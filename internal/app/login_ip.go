package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const adminPasswordFailures = 10
const adminIPFreeze = 10 * time.Minute

type AdminLoginIP struct {
	IPHash         string `gorm:"column:ip_hash;primaryKey;size:64"`
	FailedAttempts int    `gorm:"column:failed_attempts"`
	LockedUntilMS  int64  `gorm:"column:locked_until_ms"`
	UpdatedAtMS    int64  `gorm:"column:updated_at_ms"`
}

func (AdminLoginIP) TableName() string { return "admin_login_ip" }

type adminUnixPeerKey struct{}

func adminConnectionContext(ctx context.Context, connection net.Conn) context.Context {
	return context.WithValue(ctx, adminUnixPeerKey{}, connection.RemoteAddr().Network() == "unix")
}
func adminRequestIP(c *gin.Context) (string, error) {
	raw := c.ClientIP()
	if trusted, _ := c.Request.Context().Value(adminUnixPeerKey{}).(bool); trusted {
		// Only the private Unix peer (Nginx) may supply a replacement address.
		// proxy_params overwrites X-Real-IP with its verified client address.
		raw = c.GetHeader("X-Real-IP")
	}
	address, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || address.Zone() != "" {
		return "", errors.New("invalid client address")
	}
	return address.Unmap().String(), nil
}

// adminPasswordAttempt serializes password decisions for each IP in MySQL.
// Account switching, concurrent requests and process restarts cannot reset a
// freeze. A correct password clears the consecutive-password-error counter.
func (s *Server) adminPasswordAttempt(c *gin.Context, password string, lookup func(*gorm.DB, *AdminUser) error) (AdminUser, bool, bool) {
	var user AdminUser
	ip, err := adminRequestIP(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "无法确认登录来源 IP"})
		return user, false, false
	}
	sum := sha256.Sum256([]byte("skygo-admin-login:" + ip))
	row := AdminLoginIP{IPHash: fmt.Sprintf("%x", sum[:])}
	var passwordOK bool
	var captchaInvalid bool
	var blockedUntil int64
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "ip_hash = ?", row.IPHash).Error; err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if row.LockedUntilMS > now {
			blockedUntil = row.LockedUntilMS
			return nil
		}
		if row.LockedUntilMS != 0 {
			row.LockedUntilMS = 0
			row.FailedAttempts = 0
		}
		if input, exists := c.Get("login_captcha_input"); exists {
			valid, err := s.consumeLoginCaptcha(tx, row.IPHash, input.(loginCaptchaInput), now)
			if err != nil {
				return err
			}
			if !valid {
				captchaInvalid = true
				return nil
			}
		}
		err := lookup(tx, &user)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			passwordOK = verifySecret(user.PasswordHash, password)
		} else {
			_ = verifySecret("$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		}
		now = time.Now().UnixMilli()
		if passwordOK {
			row.FailedAttempts = 0
		} else {
			row.FailedAttempts++
		}
		if row.FailedAttempts >= adminPasswordFailures {
			row.LockedUntilMS = now + adminIPFreeze.Milliseconds()
			blockedUntil = row.LockedUntilMS
		}
		row.UpdatedAtMS = now
		return tx.Model(&row).Updates(map[string]any{"failed_attempts": row.FailedAttempts, "locked_until_ms": row.LockedUntilMS, "updated_at_ms": row.UpdatedAtMS}).Error
	})
	if err != nil {
		c.JSON(503, gin.H{"error": "登录保护服务暂不可用，请稍后重试"})
		return user, false, false
	}
	if captchaInvalid {
		c.JSON(400, gin.H{"error": "图片验证码无效或已过期，请重新获取", "code": "login_captcha_invalid"})
		return user, false, false
	}
	if blockedUntil != 0 {
		seconds := (blockedUntil - time.Now().UnixMilli() + 999) / 1000
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "该 IP 连续 10 次密码错误，已冻结登录 10 分钟", "retry_after_seconds": seconds})
		return user, false, false
	}
	return user, passwordOK, true
}

// TOTP failures keep the existing account protection, independently from the
// requested ten-password-error IP freeze.
func (s *Server) recordAdminOTPFailure(id uint32) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var user AdminUser
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, id).Error; err != nil {
			return err
		}
		attempts := user.FailedAttempts + 1
		locked := user.LockedUntilMS
		if attempts >= 5 {
			attempts = 0
			locked = time.Now().Add(15 * time.Minute).UnixMilli()
		}
		return tx.Model(&user).Updates(map[string]any{"failed_attempts": attempts, "locked_until_ms": locked, "updated_at_ms": time.Now().UnixMilli()}).Error
	})
}
