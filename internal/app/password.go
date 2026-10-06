package app

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ValidateAdminPassword applies the administrator password length limits.
func ValidateAdminPassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 || strings.ContainsAny(password, "\r\n\x00") {
		return errors.New("password must contain at least 12 characters, at most 1024 bytes, and no line breaks or NUL")
	}
	return nil
}

// ChangeAdminPassword is a privileged local recovery operation. It never creates
// an account, enables a disabled account, or changes roles and MFA credentials.
func ChangeAdminPassword(db *gorm.DB, username, password string) error {
	if len(username) < 3 || len(username) > 64 {
		return errors.New("invalid administrator username")
	}
	if err := ValidateAdminPassword(password); err != nil {
		return err
	}
	var version SchemaVersion
	if db.First(&version, 1).Error != nil || version.Version != 6 {
		return errors.New("management schema is incompatible; use the matching admin-api version")
	}
	hash, err := hashSecret(password)
	if err != nil {
		return errors.New("password hashing failed")
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var auditLock AuditLock
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&auditLock, 1).Error; err != nil {
			return err
		}
		var user AdminUser
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("username = ?", username).First(&user).Error; err != nil {
			return err
		}
		if err := tx.Model(&user).Updates(map[string]any{"password_hash": hash, "updated_at_ms": time.Now().UnixMilli()}).Error; err != nil {
			return err
		}
		result := tx.Where("admin_id = ?", user.ID).Delete(&Session{})
		if result.Error != nil {
			return result.Error
		}
		return (&Server{}).appendAudit(tx, 0, "admin.password.change.cli", strconv.FormatUint(uint64(user.ID), 10), map[string]any{"source": "local_cli", "sessions_revoked": result.RowsAffected})
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("administrator or audit state not found; no password changed")
	}
	if err != nil {
		return errors.New("password change failed; transaction rolled back")
	}
	return nil
}

var errStaleLogin = errors.New("login credentials changed")

// storeLoginSession shares the account lock with password changes. A login that
// verified the old hash cannot create a session after the password was changed.
func (s *Server) storeLoginSession(expected AdminUser, session Session) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var current AdminUser
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, expected.ID).Error; err != nil {
			return err
		}
		if !current.Active || current.PasswordHash != expected.PasswordHash || current.Role != expected.Role || current.TOTPSecret != expected.TOTPSecret || current.LockedUntilMS > time.Now().UnixMilli() {
			return errStaleLogin
		}
		if err := tx.Model(&current).Updates(map[string]any{"failed_attempts": 0, "locked_until_ms": 0, "updated_at_ms": time.Now().UnixMilli()}).Error; err != nil {
			return err
		}
		return tx.Create(&session).Error
	})
}
