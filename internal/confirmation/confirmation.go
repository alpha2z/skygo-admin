// Package confirmation provides durable, operation-bound email challenges.
package confirmation

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalid = errors.New("验证码无效、已过期或操作已变更")
var ErrRate = errors.New("请求过于频繁，请稍后重试")
var ErrPending = errors.New("操作正在执行或执行结果待核对，请勿重复执行")

type Challenge struct {
	SecondEmail    string    `gorm:"size:254"`
	SecondCodeHash string    `gorm:"size:64"`
	ID             string    `gorm:"primaryKey;size:64"`
	AdminID        uint32    `gorm:"index"`
	Digest         string    `gorm:"size:64"`
	CodeHash       string    `gorm:"size:64"`
	Email          string    `gorm:"size:254"`
	IPHash         string    `gorm:"size:64;index"`
	CreatedAt      time.Time `gorm:"index"`
	ExpiresAt      time.Time
	Attempts       int
	Status         string `gorm:"size:20"`
	Response       string `gorm:"type:mediumtext"`
	ResponseCode   int
}

func (Challenge) TableName() string { return "admin_confirmation" }

type Rate struct {
	Key      string `gorm:"primaryKey;size:80"`
	WindowAt time.Time
	LastAt   time.Time
	Count    int
}

func (Rate) TableName() string { return "admin_confirmation_rate" }

type Sender interface {
	Send(context.Context, string, string, string) error
}
type Service struct {
	DB     *gorm.DB
	Secret []byte
	Sender Sender
}

func (s *Service) hash(value string) string {
	h := hmac.New(sha256.New, s.Secret)
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}
func Digest(method, path string, body []byte) string {
	sum := sha256.Sum256(append([]byte(method+"\n"+path+"\n"), body...))
	return hex.EncodeToString(sum[:])
}
func (s *Service) throttle(tx *gorm.DB, key string, limit int, spacing time.Duration, now time.Time) error {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Rate{Key: key, WindowAt: now, LastAt: time.Unix(0, 0)}).Error; err != nil {
		return err
	}
	var r Rate
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, "`key` = ?", key).Error; err != nil {
		return err
	}
	if now.Sub(r.LastAt) < spacing {
		return ErrRate
	}
	if now.Sub(r.WindowAt) >= time.Hour {
		r.WindowAt = now
		r.Count = 0
	}
	if r.Count >= limit {
		return ErrRate
	}
	r.Count++
	r.LastAt = now
	return tx.Save(&r).Error
}
func (s *Service) Issue(ctx context.Context, admin uint32, email, ip, digest, summary string, secondEmail ...string) (Challenge, error) {
	if s.DB == nil || s.Sender == nil || len(s.Secret) < 32 {
		return Challenge{}, errors.New("邮件确认服务未配置")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return Challenge{}, err
	}
	code := fmt.Sprintf("%08d", n.Int64())
	id := make([]byte, 24)
	if _, err = rand.Read(id); err != nil {
		return Challenge{}, err
	}
	now := time.Now().UTC()
	row := Challenge{ID: hex.EncodeToString(id), AdminID: admin, Email: email, IPHash: s.hash(ip), Digest: digest, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute), Status: "sending"}
	row.CodeHash = s.hash(row.ID + ":" + code)
	secondCode := ""
	if len(secondEmail) > 0 && secondEmail[0] != "" {
		second, err := rand.Int(rand.Reader, big.NewInt(100000000))
		if err != nil {
			return Challenge{}, err
		}
		secondCode = fmt.Sprintf("%08d", second.Int64())
		row.SecondEmail = secondEmail[0]
		row.SecondCodeHash = s.hash(row.ID + ":old:" + secondCode)
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.throttle(tx, "admin:"+fmt.Sprint(admin), 60, 0, now); err != nil {
			return err
		}
		if err := s.throttle(tx, "ip:"+row.IPHash, 240, 0, now); err != nil {
			return err
		}
		if err := s.throttle(tx, "retry:"+s.hash(fmt.Sprint(admin)+":"+digest), 60, 60*time.Second, now); err != nil {
			return err
		}
		if err := tx.Model(&Challenge{}).Where("admin_id = ? AND digest = ? AND status IN ?", admin, digest, []string{"sending", "pending"}).Update("status", "superseded").Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return Challenge{}, err
	}
	// Never store the plaintext code or SMTP error (which may contain credentials).
	err = s.Sender.Send(ctx, email, "Skygo 管理操作确认", summary+"\n\n验证码："+code+"\n10 分钟内有效。仅确认邮件描述的操作；若非本人申请，请勿回填。")
	if err == nil && row.SecondEmail != "" {
		err = s.Sender.Send(ctx, row.SecondEmail, "Skygo 管理员邮箱换绑确认", summary+"\n原邮箱验证码："+secondCode+"\n10 分钟内有效，非本人操作请勿提供。")
	}
	status := "pending"
	if err != nil {
		status = "send_failed"
	}
	result := s.DB.Model(&Challenge{}).Where("id = ? AND status = ?", row.ID, "sending").Update("status", status)
	if err != nil {
		return Challenge{}, errors.New("确认邮件发送失败，请稍后重试")
	}
	if result.Error != nil || result.RowsAffected != 1 {
		return Challenge{}, errors.New("确认邮件状态未保存，请稍后重试")
	}
	row.Status = status
	return row, nil
}

// Begin atomically consumes a code and persists the execution intent. A crash
// after this point must not cause blind re-execution of an external side effect.
func (s *Service) Begin(ctx context.Context, admin uint32, id, code, digest string) (Challenge, bool, error) {
	return s.begin(ctx, admin, id, code, digest, nil)
}
func (s *Service) Apply(ctx context.Context, admin uint32, id, code, digest string, apply func(*gorm.DB, Challenge) error) (Challenge, bool, error) {
	return s.begin(ctx, admin, id, code, digest, apply)
}
func (s *Service) begin(ctx context.Context, admin uint32, id, code, digest string, apply func(*gorm.DB, Challenge) error) (Challenge, bool, error) {
	var row Challenge
	var denied error
	replay := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ? AND admin_id = ?", id, admin).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalid
			}
			return err
		}
		if row.Digest != digest {
			return ErrInvalid
		}
		if row.Status == "done" {
			plain, err := openResponse(s.Secret, row.ID, row.Response)
			if err != nil {
				return err
			}
			row.Response = plain
			replay = true
			return nil
		}
		if row.Status == "executing" {
			return ErrPending
		}
		if row.Status != "pending" || row.Attempts >= 5 || !time.Now().Before(row.ExpiresAt) {
			return ErrInvalid
		}
		row.Attempts++
		parts := strings.Split(code, ":")
		valid := len(parts[0]) == 8 && subtle.ConstantTimeCompare([]byte(row.CodeHash), []byte(s.hash(id+":"+parts[0]))) == 1
		if row.SecondEmail != "" {
			valid = valid && len(parts) == 2
			if len(parts) == 2 {
				valid = valid && len(parts[1]) == 8 && subtle.ConstantTimeCompare([]byte(row.SecondCodeHash), []byte(s.hash(id+":old:"+parts[1]))) == 1
			}
		} else {
			valid = valid && len(parts) == 1
		}
		if !valid {
			denied = ErrInvalid
			if row.Attempts >= 5 {
				row.Status = "locked"
			}
			return tx.Save(&row).Error
		}
		row.Status = "executing"
		if apply != nil {
			if err := apply(tx, row); err != nil {
				return err
			}
			row.Status = "done"
			row.ResponseCode = 200
			sealed, err := SealResponse(s.Secret, row.ID, `{"verified":true}`)
			if err != nil {
				return err
			}
			row.Response = sealed
		}
		row.CodeHash = ""
		row.SecondCodeHash = ""
		return tx.Save(&row).Error
	})
	if err != nil {
		return row, false, err
	}
	return row, replay, denied
}
func (s *Service) Finish(ctx context.Context, id string, status int, body string) error {
	if len(body) > 1<<20 {
		return errors.New("confirmation response too large")
	}
	sealed, err := SealResponse(s.Secret, id, body)
	if err != nil {
		return err
	}
	result := s.DB.WithContext(ctx).Model(&Challenge{}).Where("id = ? AND status = ?", id, "executing").Updates(map[string]any{"status": "done", "response_code": status, "response": sealed})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPending
	}
	return nil
}
