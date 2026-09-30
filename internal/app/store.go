package app

import (
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/confirmation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type Session struct {
	ID        string `gorm:"primaryKey;size:64"`
	AdminID   uint32
	CSRF      string `gorm:"size:64"`
	ExpiresAt time.Time
}
type AuditLock struct {
	ID uint8 `gorm:"primaryKey"`
}
type SchemaVersion struct {
	ID      uint8 `gorm:"primaryKey"`
	Version int
}
type Host struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	TokenHash    string    `gorm:"size:64" json:"-"`
	Active       bool      `json:"active"`
	BootID       string    `gorm:"size:64" json:"boot_id"`
	LastSeen     time.Time `json:"last_seen"`
	Observations string    `gorm:"type:text" json:"observations"`
}
type ServiceRecord struct {
	ID         string `gorm:"primaryKey;size:64" json:"id"`
	Definition string `gorm:"type:text" json:"definition"`
	BusyTask   string `gorm:"size:64" json:"busy_task"`
}
type Task struct {
	DeliveryID    string     `gorm:"size:64;index" json:"delivery_id,omitempty"`
	PublicationID string     `gorm:"size:64;index" json:"publication_id,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	ID            string     `gorm:"primaryKey;size:64" json:"id"`
	HostID        string     `gorm:"size:64;index" json:"host_id"`
	ServiceID     string     `gorm:"size:64;index" json:"service_id"`
	Action        string     `gorm:"size:32" json:"action"`
	Payload       string     `gorm:"type:mediumtext" json:"-"`
	PayloadHash   string     `gorm:"size:64" json:"payload_hash"`
	Status        string     `gorm:"size:32;index" json:"status"`
	RequestedBy   uint32     `json:"requested_by"`
	ApprovedBy    uint32     `json:"approved_by"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	Result        string     `gorm:"type:mediumtext" json:"result"`
	ConfigVersion string     `gorm:"size:64" json:"config_version,omitempty"`
}
type ConfigRelease struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	ServiceID   string    `gorm:"size:64;index" json:"service_id"`
	Content     string    `gorm:"type:mediumtext" json:"content"`
	SHA256      string    `gorm:"size:64" json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	RequestedBy uint32    `json:"requested_by"`
	Active      bool      `json:"active"`
	Kind        string    `gorm:"size:20" json:"kind"`
}
type GitHubDispatch struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	Status    string    `gorm:"size:32" json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Migrate is an explicit administrator action, never part of ordinary startup.
func Migrate(db *gorm.DB) error {
	if db.Migrator().HasTable(&SchemaVersion{}) {
		var existing SchemaVersion
		if err := db.First(&existing, 1).Error; err == nil && existing.Version > 4 {
			return errors.New("newer schema detected; downgrade refused")
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("schema version unavailable")
		}
	}
	if err := db.AutoMigrate(&SchemaVersion{}, &AdminUser{}, &AdminBootstrapState{}, &RecoveryCode{}, &RolePolicy{}, &Audit{}, &AuditLock{}, &AdminLoginIP{}, &AdminLoginCaptcha{}, &Session{}, &Host{}, &ServiceRecord{}, &Task{}, &ConfigRelease{}, &GitHubDispatch{}, &BuildTrustKey{}, &ImageRelease{}, &ImagePreparation{}, &Publication{}, &ImageDelivery{}, &ImageAttempt{}, &ImageCache{}, &ImageCleanup{}, &confirmation.Challenge{}, &confirmation.Rate{}); err != nil {
		return errors.New("schema migration failed")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&AuditLock{ID: 1}).Error; err != nil {
			return err
		}
		return tx.Save(&SchemaVersion{ID: 1, Version: 4}).Error
	})
}
func (s *Server) appendAudit(tx *gorm.DB, operator uint32, action, target string, detail any) error {
	var lock AuditLock
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error; err != nil {
		return err
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var previous Audit
	hash := strings.Repeat("0", 64)
	err = tx.Order("audit_id DESC").First(&previous).Error
	if err == nil {
		hash = previous.EntryHash
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	now := time.Now().UnixMilli()
	return tx.Create(&Audit{OperatorID: operator, Action: action, Target: target, DetailJSON: string(payload), PreviousHash: hash, EntryHash: chainHash(hash, operator, action, target, string(payload), now), CreatedAtMS: now}).Error
}
