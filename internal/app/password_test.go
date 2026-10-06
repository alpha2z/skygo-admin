package app

import (
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdministratorPasswordValidation(t *testing.T) {
	for _, password := range []string{"", "short", strings.Repeat("x", 1025), "synthetic\npassword", string([]byte{0xff}) + strings.Repeat("x", 20)} {
		if ValidateAdminPassword(password) == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if err := ValidateAdminPassword("十二个字的测试口令与密码"); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPasswordChangeRevokesSessionsAndSerializesLoginMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	oldPassword, newPassword := "synthetic-original-password", "synthetic-new-password"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": oldPassword}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	first := signIn(t, s, r, "owner", oldPassword)
	second := signIn(t, s, r, "owner", oldPassword)
	var original AdminUser
	if db.Where("username = ?", "owner").First(&original).Error != nil {
		t.Fatal("fixture user")
	}
	other := Session{ID: "unrelated", AdminID: 999, CSRF: "fixture", ExpiresAt: time.Now().Add(time.Hour)}
	if db.Create(&other).Error != nil {
		t.Fatal("fixture session")
	}
	if err = ChangeAdminPassword(db, "owner", newPassword); err != nil {
		t.Fatal(err)
	}
	check(t, call(r, first, "GET", "/api/v1/session", nil, nil), 401)
	check(t, call(r, second, "GET", "/api/v1/session", nil, nil), 401)
	var current AdminUser
	db.First(&current, original.ID)
	if verifySecret(current.PasswordHash, oldPassword) || !verifySecret(current.PasswordHash, newPassword) {
		t.Fatal("password hash not replaced")
	}
	if current.Role != original.Role || current.TOTPSecret != original.TOTPSecret || current.Active != original.Active {
		t.Fatal("unrelated account properties changed")
	}
	var sessions int64
	db.Model(&Session{}).Where("admin_id = ?", 999).Count(&sessions)
	if sessions != 1 {
		t.Fatal("unrelated session revoked")
	}
	// Model the final step of a login that verified the old hash before reset.
	err = s.storeLoginSession(original, Session{ID: "stale", AdminID: original.ID, CSRF: "stale", ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, errStaleLogin) {
		t.Fatal("old-password login survived reset", err)
	}
	signIn(t, s, r, "owner", newPassword)
	var audit Audit
	if db.Where("action = ?", "admin.password.change.cli").First(&audit).Error != nil {
		t.Fatal("missing audit")
	}
	if audit.OperatorID != 0 || strings.Contains(audit.DetailJSON, newPassword) || strings.Contains(audit.DetailJSON, oldPassword) || strings.Contains(audit.DetailJSON, current.PasswordHash) {
		t.Fatal("audit leaked credentials or impersonated operator")
	}
	if err = ChangeAdminPassword(db, "missing", newPassword); err == nil {
		t.Fatal("unknown user accepted")
	}
	db.Model(&AdminUser{}).Where("id = ?", original.ID).Updates(map[string]any{"active": false, "failed_attempts": 3, "locked_until_ms": time.Now().Add(time.Minute).UnixMilli()})
	if err = ChangeAdminPassword(db, "owner", "synthetic-next-password"); err != nil {
		t.Fatal(err)
	}
	db.First(&current, original.ID)
	if current.Active || current.FailedAttempts != 3 || current.LockedUntilMS == 0 {
		t.Fatal("password reset bypassed account protection")
	}
}

func TestPasswordChangeAuditFailureRollsBackMySQL(t *testing.T) {
	db, _ := releaseDB(t)
	hash, _ := hashSecret("synthetic-original-password")
	user := AdminUser{Username: "owner", PasswordHash: hash, Role: "superadmin", Active: true}
	if db.Create(&user).Error != nil {
		t.Fatal("fixture")
	}
	session := Session{ID: "retained", AdminID: user.ID, ExpiresAt: time.Now().Add(time.Hour)}
	db.Create(&session)
	if err := db.Migrator().DropTable(&Audit{}); err != nil {
		t.Fatal(err)
	}
	if err := ChangeAdminPassword(db, "owner", "synthetic-new-password"); err == nil {
		t.Fatal("unaudited reset committed")
	}
	var retained AdminUser
	db.First(&retained, user.ID)
	if retained.PasswordHash != hash {
		t.Fatal("hash not rolled back")
	}
	var count int64
	db.Model(&Session{}).Where("id = ?", session.ID).Count(&count)
	if count != 1 {
		t.Fatal("session revocation not rolled back")
	}
}

func TestPasswordCLIOnlyNeedsManagementDSNMySQL(t *testing.T) {
	db, _ := releaseDB(t)
	hash, _ := hashSecret("synthetic-cli-original")
	user := AdminUser{Username: "operator", PasswordHash: hash, Role: "operator", Active: true}
	if db.Create(&user).Error != nil {
		t.Fatal("CLI user fixture")
	}
	parsed, err := mysql.ParseDSN(os.Getenv("SKYGO_ADMIN_TEST_DSN"))
	if err != nil {
		t.Fatal("fixture DSN")
	}
	var database string
	if db.Raw("SELECT DATABASE()").Scan(&database).Error != nil {
		t.Fatal("fixture database")
	}
	parsed.DBName = database
	dir := t.TempDir()
	dsnPath, passwordPath := filepath.Join(dir, "dsn"), filepath.Join(dir, "password")
	if os.WriteFile(dsnPath, []byte(parsed.FormatDSN()), 0600) != nil || os.WriteFile(passwordPath, []byte("synthetic-cli-new-password\n"), 0600) != nil {
		t.Fatal("private fixture files")
	}
	cmd := exec.Command("go", "run", "../../cmd/admin-api", "-change-password", "operator", "-password-file", passwordPath)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "ADMIN_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "ADMIN_MYSQL_DSN_FILE="+dsnPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal("CLI execution failed", err)
	}
	if strings.Contains(string(output), "synthetic-cli-new-password") || strings.Contains(string(output), parsed.FormatDSN()) {
		t.Fatal("CLI output exposed credentials")
	}
	var current AdminUser
	db.First(&current, user.ID)
	if !verifySecret(current.PasswordHash, "synthetic-cli-new-password") {
		t.Fatal("CLI did not update password")
	}
}
