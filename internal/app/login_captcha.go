package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A single IP-bound challenge survives restarts. Only its keyed hash is stored.
type AdminLoginCaptcha struct {
	IPHash      string `gorm:"primaryKey;size:64"`
	ChallengeID string `gorm:"size:64"`
	AnswerHash  string `gorm:"size:64"`
	IssuedAtMS  int64
	ExpiresAtMS int64
	Consumed    bool
}

func (AdminLoginCaptcha) TableName() string { return "admin_login_captcha" }

type loginCaptchaInput struct{ ID, Code string }

func loginIPHash(ip string) string {
	sum := sha256.Sum256([]byte("skygo-admin-login:" + ip))
	return hex.EncodeToString(sum[:])
}
func (s *Server) captchaHash(ip, id, code string) string {
	h := hmac.New(sha256.New, []byte(s.cfg.JWTSecret))
	fmt.Fprintf(h, "login-captcha\x00%s\x00%s\x00%s", ip, id, code)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) consumeLoginCaptcha(tx *gorm.DB, ip string, input loginCaptchaInput, now int64) (bool, error) {
	if input.ID == "" || len(input.ID) > 64 || len(input.Code) != 6 {
		return false, nil
	}
	var row AdminLoginCaptcha
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "ip_hash = ?", ip).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.Consumed || row.ChallengeID != input.ID || row.ExpiresAtMS <= now {
		return false, nil
	}
	valid := subtle.ConstantTimeCompare([]byte(row.AnswerHash), []byte(s.captchaHash(ip, input.ID, input.Code))) == 1
	// A submitted challenge is single use even when the answer/password is wrong.
	err = tx.Model(&row).Update("consumed", true).Error
	return valid, err
}
func (s *Server) issueLoginCaptcha(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ip, err := adminRequestIP(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "Unable to determine the login source IP"})
		return
	}
	if s.db == nil {
		c.JSON(503, gin.H{"error": "Login captcha service is temporarily unavailable"})
		return
	}
	hash := loginIPHash(ip)
	var id, code string
	var issued, expires, retry int64
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// Same lock order as password validation: IP first, challenge second.
		rate := AdminLoginIP{IPHash: hash}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rate).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&rate, "ip_hash = ?", hash).Error; err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if rate.LockedUntilMS > now {
			retry = rate.LockedUntilMS - now
			return nil
		}
		row := AdminLoginCaptcha{IPHash: hash}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "ip_hash = ?", hash).Error; err != nil {
			return err
		}
		interval := int64(1000) // basic refresh protection before three password failures
		if rate.FailedAttempts >= 3 && rate.LockedUntilMS == 0 {
			interval = 60000
		}
		if now < row.IssuedAtMS+interval {
			retry = row.IssuedAtMS + interval - now
			return nil
		}
		token, err := randomToken(24)
		if err != nil {
			return err
		}
		id = token
		for i := 0; i < 6; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(10))
			if err != nil {
				return err
			}
			code += strconv.Itoa(int(n.Int64()))
		}
		issued = now
		expires = now + 120000
		row.ChallengeID = id
		row.AnswerHash = s.captchaHash(hash, id, code)
		row.IssuedAtMS = issued
		row.ExpiresAtMS = expires
		row.Consumed = false
		return tx.Save(&row).Error
	})
	if err != nil {
		c.JSON(503, gin.H{"error": "Login captcha service is temporarily unavailable"})
		return
	}
	if retry > 0 {
		seconds := (retry + 999) / 1000
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		c.JSON(429, gin.H{"error": "Captcha requests are too frequent; wait until the countdown ends", "code": "login_captcha_rate_limited", "retry_after_seconds": seconds})
		return
	}
	picture, err := captchaPNG(code)
	if err != nil {
		c.JSON(503, gin.H{"error": "Failed to generate captcha"})
		return
	}
	c.JSON(200, gin.H{"captcha_id": id, "image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(picture), "expires_at_ms": expires})
}

// Raster-only digits: the response never contains a text/SVG answer.
func captchaPNG(code string) ([]byte, error) {
	glyphs := []string{"01110100011001110101110011000101110", "00100011000010000100001000010001110", "01110100010000100010001000100011111", "11110000010000101110000010000111110", "00010001100101010010111110001000010", "11111100001000011110000010000111110", "01110100001000011110100011000101110", "11111000010001000100010000100001000", "01110100011000101110100011000101110", "01110100011000101111000010000101110"}
	img := image.NewRGBA(image.Rect(0, 0, 216, 64))
	bg := color.RGBA{245, 246, 250, 255}
	for y := 0; y < 64; y++ {
		for x := 0; x < 216; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	var noise [1024]byte
	if _, err := rand.Read(noise[:]); err != nil {
		return nil, err
	}
	for i := 0; i < 250; i++ {
		img.SetRGBA(int(noise[i*2])%216, int(noise[i*2+1])%64, color.RGBA{160, 178, 190, 255})
	}
	for i, d := range []byte(code) {
		if d < '0' || d > '9' {
			return nil, errors.New("invalid digit")
		}
		x0 := 8 + i*34 + int(noise[600+i]%4)
		y0 := 10 + int(noise[620+i]%12)
		ink := color.RGBA{uint8(20 + noise[640+i]%60), uint8(25 + noise[660+i]%60), uint8(35 + noise[680+i]%60), 255}
		for j, v := range glyphs[d-'0'] {
			if v != '1' {
				continue
			}
			for dy := 0; dy < 5; dy++ {
				for dx := 0; dx < 5; dx++ {
					img.SetRGBA(x0+(j%5)*5+dx+(j/5-3)/2, y0+(j/5)*5+dy, ink)
				}
			}
		}
	}
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}
