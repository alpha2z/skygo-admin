package app

import (
	"context"
	"crypto/subtle"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/confirmation"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) bootstrapStatus(c *gin.Context) {
	configured := strings.TrimSpace(s.cfg.BootstrapToken) != ""
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"enabled": false, "needs_bootstrap": false, "error": "admin database unavailable"})
		return
	}
	var admins int64
	if err := s.db.Model(&AdminUser{}).Count(&admins).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"enabled": false, "needs_bootstrap": false, "error": "admin database unavailable"})
		return
	}
	claimed := false
	if admins == 0 {
		var state AdminBootstrapState
		err := s.db.Where("id = ?", 1).Take(&state).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			c.JSON(http.StatusServiceUnavailable, gin.H{"enabled": false, "needs_bootstrap": false, "error": "admin database unavailable"})
			return
		}
		claimed = state.ClaimedAtMS != 0
	}
	needsBootstrap := configured && admins == 0 && !claimed
	c.JSON(http.StatusOK, gin.H{"enabled": needsBootstrap, "needs_bootstrap": needsBootstrap})
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		c.Next()
	}
}
func (s *Server) setCookies(c *gin.Context, token, csrf string) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("admin_session", token, 7200, "/", "", s.cfg.CookieSecure, true)
	c.SetCookie("admin_csrf", csrf, 7200, "/", "", s.cfg.CookieSecure, false)
}

func (s *Server) bootstrap(c *gin.Context) {
	if s.db == nil || s.cfg.BootstrapToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Bootstrap-Token")), []byte(s.cfg.BootstrapToken)) != 1 {
		c.JSON(403, gin.H{"error": "bootstrap disabled"})
		return
	}
	var req struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Username) < 3 || len(req.Username) > 64 || len(req.Password) > 1024 || !confirmation.ValidEmail(req.Email) || utf8.RuneCountInString(req.Password) < 12 {
		c.JSON(400, gin.H{"error": "username or password invalid"})
		return
	}
	hash, err := hashSecret(req.Password)
	if err != nil {
		c.JSON(500, gin.H{"error": "security"})
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Skygo Admin", AccountName: req.Username})
	if err != nil {
		c.JSON(500, gin.H{"error": "totp"})
		return
	}
	now := time.Now().UnixMilli()
	user := AdminUser{Email: req.Email, Username: req.Username, PasswordHash: hash, TOTPSecret: key.Secret(), Role: "superadmin", Active: true, CreatedAtMS: now, UpdatedAtMS: now}
	var recovery []string
	permissions := allPermissions
	disabled := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&AdminBootstrapState{ID: 1}).Error; err != nil {
			return err
		}
		var state AdminBootstrapState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		if state.ClaimedAtMS != 0 {
			disabled = true
			return nil
		}
		var count int64
		if err := tx.Model(&AdminUser{}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			disabled = true
			return tx.Model(&state).Updates(map[string]any{"claimed_at_ms": now}).Error
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		for _, permission := range permissions {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RolePolicy{Role: "superadmin", Permission: permission}).Error; err != nil {
				return err
			}
		}
		for i := 0; i < 10; i++ {
			code, err := randomToken(12)
			if err != nil {
				return err
			}
			hashed, err := hashSecret(code)
			if err != nil {
				return err
			}
			recovery = append(recovery, code)
			if err := tx.Create(&RecoveryCode{UserID: user.ID, CodeHash: hashed}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&state).Updates(map[string]any{"admin_user_id": user.ID, "claimed_at_ms": now}).Error; err != nil {
			return err
		}
		return s.appendAudit(tx, user.ID, "bootstrap", req.Username, map[string]any{"role": "superadmin"})
	})
	if err != nil {
		c.JSON(409, gin.H{"error": "bootstrap failed"})
		return
	}
	if disabled {
		c.JSON(403, gin.H{"error": "bootstrap disabled"})
		return
	}
	for _, permission := range permissions {
		_, _ = s.auth.AddPolicy("superadmin", permission)
	}
	_, _ = s.auth.AddGroupingPolicy("superadmin", "superadmin")
	c.JSON(201, gin.H{"totp_uri": key.URL(), "recovery_codes": recovery})
}

func (s *Server) login(c *gin.Context) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Code        string `json:"code"`
		CaptchaID   string `json:"captcha_id"`
		CaptchaCode string `json:"captcha_code"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Password) > 1024 || len(req.Username) > 64 {
		c.JSON(400, gin.H{"error": "invalid request"})
		return
	}
	c.Set("login_captcha_input", loginCaptchaInput{req.CaptchaID, req.CaptchaCode})
	user, passwordOK, proceed := s.adminPasswordAttempt(c, req.Password, func(tx *gorm.DB, user *AdminUser) error {
		return tx.Where("username = ?", req.Username).First(user).Error
	})
	if !proceed {
		return
	}
	if !passwordOK || !user.Active || user.LockedUntilMS > time.Now().UnixMilli() {
		c.JSON(401, gin.H{"error": "invalid credentials"})
		return
	}
	valid := !s.cfg.TOTPEnabled || totp.Validate(req.Code, user.TOTPSecret)
	if s.cfg.TOTPEnabled && !valid {
		valid = s.consumeRecovery(&user, req.Code)
	}
	if !valid {
		if err := s.recordAdminOTPFailure(user.ID); err != nil {
			c.JSON(503, gin.H{"error": "Login protection is temporarily unavailable"})
			return
		}
		c.JSON(401, gin.H{"error": "invalid credentials"})
		return
	}
	if err := s.db.Model(&user).Updates(map[string]any{"failed_attempts": 0, "locked_until_ms": 0, "updated_at_ms": time.Now().UnixMilli()}).Error; err != nil {
		c.JSON(503, gin.H{"error": "Login service is temporarily unavailable"})
		return
	}
	expires := time.Now().Add(2 * time.Hour)
	sid, err := randomToken(24)
	if err != nil {
		c.Status(500)
		return
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{Role: user.Role, RegisteredClaims: jwt.RegisteredClaims{ID: sid, Subject: strconv.FormatUint(uint64(user.ID), 10), Issuer: "skygo-admin", ExpiresAt: jwt.NewNumericDate(expires), IssuedAt: jwt.NewNumericDate(time.Now())}})
	signed, err := token.SignedString([]byte(s.cfg.JWTSecret))
	csrf, _ := randomToken(24)
	if err != nil {
		c.JSON(500, gin.H{"error": "token"})
		return
	}
	if s.db.Create(&Session{ID: sid, AdminID: user.ID, CSRF: csrf, ExpiresAt: expires}).Error != nil {
		c.Status(503)
		return
	}
	s.setCookies(c, signed, csrf)
	c.JSON(200, gin.H{"id": user.ID, "role": user.Role, "csrf_token": csrf, "expires_at_ms": expires.UnixMilli()})
}

func (s *Server) consumeRecovery(user *AdminUser, code string) bool {
	var values []RecoveryCode
	if s.db.Where("user_id=? AND used_at_ms=0", user.ID).Find(&values).Error != nil {
		return false
	}
	for _, value := range values {
		if verifySecret(value.CodeHash, code) {
			return s.db.Model(&RecoveryCode{}).Where("user_id=? AND code_hash=? AND used_at_ms=0", user.ID, value.CodeHash).Update("used_at_ms", time.Now().UnixMilli()).RowsAffected == 1
		}
	}
	return false
}
func (s *Server) authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie("admin_session")
		if err != nil {
			c.AbortWithStatus(401)
			return
		}
		parsed, err := jwt.ParseWithClaims(raw, &claims{}, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("invalid algorithm")
			}
			return []byte(s.cfg.JWTSecret), nil
		}, jwt.WithIssuer("skygo-admin"), jwt.WithExpirationRequired())
		if err != nil || !parsed.Valid {
			c.AbortWithStatus(401)
			return
		}
		current := parsed.Claims.(*claims)
		id, err := strconv.ParseUint(current.Subject, 10, 32)
		if err != nil || id == 0 || s.sessionOK == nil || !s.sessionOK(c.Request.Context(), uint32(id), current.Role) {
			c.AbortWithStatus(401)
			return
		}
		var session Session
		if s.db.First(&session, "id = ? AND admin_id = ? AND expires_at > ?", current.ID, id, time.Now()).Error != nil {
			c.AbortWithStatus(401)
			return
		}
		c.Set("session_id", current.ID)
		c.Set("session_csrf", session.CSRF)
		c.Set("admin_id", uint(id))
		c.Set("admin_role", current.Role)
		c.Next()
	}
}

func (s *Server) activeAdminSession(ctx context.Context, id uint32, role string) bool {
	if s.db == nil || id == 0 || role == "" {
		return false
	}
	var user AdminUser
	return s.db.WithContext(ctx).Select("id", "role", "active").Where("id = ?", id).Take(&user).Error == nil && user.Active && user.Role == role
}
func (s *Server) csrf() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		cookie, err := c.Cookie("admin_csrf")
		if err != nil || cookie == "" || c.GetHeader("X-CSRF-Token") != cookie || cookie != c.GetString("session_csrf") {
			c.AbortWithStatusJSON(403, gin.H{"error": "csrf"})
			return
		}
		c.Next()
	}
}
func (s *Server) require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, _ := s.auth.Enforce(c.GetString("admin_role"), permission)
		if !ok {
			c.AbortWithStatus(403)
			return
		}
		c.Next()
	}
}
func (s *Server) logout(c *gin.Context) {
	if s.db.Delete(&Session{}, "id = ?", c.GetString("session_id")).Error != nil {
		c.Status(503)
		return
	}
	s.setCookies(c, "", "")
	c.JSON(200, gin.H{"ok": true})
}
