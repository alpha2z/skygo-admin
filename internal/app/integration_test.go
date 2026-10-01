package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	mysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type browser struct {
	cookies []*http.Cookie
	csrf    string
}

func call(r http.Handler, b *browser, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = "127.0.0.1:23456"
	req.Header.Set("Content-Type", "application/json")
	if b != nil {
		for _, cookie := range b.cookies {
			req.AddCookie(cookie)
		}
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if b != nil && len(w.Result().Cookies()) > 0 {
		b.cookies = w.Result().Cookies()
		for _, cookie := range b.cookies {
			if cookie.Name == "admin_csrf" {
				b.csrf = cookie.Value
			}
		}
	}
	return w
}
func check(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d; expected %d", w.Code, status)
	}
}
func dbFixture(t *testing.T) (*gorm.DB, Config) {
	t.Helper()
	dsn := os.Getenv("SKYGO_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("run scripts/integration.py for isolated MySQL")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(parsed.DBName, "skygo_admin_test_") {
		t.Fatal("isolated database required")
	}
	db, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	return db, Config{JWTSecret: strings.Repeat("j", 40), BootstrapToken: strings.Repeat("b", 40), SigningKey: key, SkipEmailConfirmation: true, WebRoot: "../../admin-web"}
}
func signIn(t *testing.T, s *Server, r http.Handler, username, password string) *browser {
	t.Helper()
	ip := loginIPHash("127.0.0.1")
	id := "test-captcha-" + username
	row := AdminLoginCaptcha{IPHash: ip, ChallengeID: id, AnswerHash: s.captchaHash(ip, id, "123456"), ExpiresAtMS: time.Now().Add(time.Minute).UnixMilli()}
	if s.db.Save(&row).Error != nil {
		t.Fatal("captcha fixture")
	}
	b := &browser{}
	w := call(r, b, "POST", "/api/v1/login", map[string]string{"username": username, "password": password, "captcha_id": id, "captcha_code": "123456"}, nil)
	check(t, w, 200)
	return b
}
func TestIsolatedManagementWorkflow(t *testing.T) {
	db, cfg := dbFixture(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	check(t, call(r, nil, "GET", "/healthz", nil, nil), 200)
	check(t, call(r, nil, "GET", "/api/v1/hosts", nil, nil), 401)
	password := "test-only-long-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "password": password, "email": "owner@example.com"}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "other", "password": password, "email": "other@example.com"}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 403)
	check(t, call(r, nil, "POST", "/api/v1/login", map[string]string{"username": "owner", "password": password}, nil), 400)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "reviewer", "Password": password, "Email": "reviewer@example.com", "Role": "approver"}, nil), 200)
	reviewer := signIn(t, s, r, "reviewer", password)
	check(t, call(r, reviewer, "POST", "/api/v1/hosts", map[string]string{"id": "bad-host"}, nil), 403)
	enrolled := call(r, owner, "POST", "/api/v1/hosts", map[string]string{"id": "host-a"}, nil)
	check(t, enrolled, 200)
	var enrollment struct{ Token string }
	json.Unmarshal(enrolled.Body.Bytes(), &enrollment)
	headers := map[string]string{"X-Host-ID": "host-a", "Authorization": "Bearer " + enrollment.Token}
	image := "example/worker@sha256:" + strings.Repeat("a", 64)
	check(t, call(r, owner, "POST", "/api/v1/services", control.Service{ID: "worker", HostID: "host-a", Image: image}, nil), 200)
	check(t, call(r, nil, "POST", "/agent/v1/heartbeat", control.Heartbeat{Version: 1, BootID: "boot-a"}, headers), 200)
	taskResponse := call(r, owner, "POST", "/api/v1/tasks", map[string]string{"service": "worker", "action": "deploy", "image": image}, nil)
	check(t, taskResponse, 200)
	var task Task
	json.Unmarshal(taskResponse.Body.Bytes(), &task)
	check(t, call(r, owner, "POST", "/api/v1/tasks/"+task.ID+"/approve", nil, nil), 409)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks/"+task.ID+"/approve", nil, nil), 200)
	w := call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, w, 200)
	var envelopes []control.Envelope
	json.Unmarshal(w.Body.Bytes(), &envelopes)
	if len(envelopes) != 1 {
		t.Fatal("command missing")
	}
	cmd, err := control.Verify(envelopes[0], cfg.SigningKey.Public().(ed25519.PublicKey), "host-a")
	if err != nil || cmd.ID != task.ID {
		t.Fatal("invalid command")
	}
	// A controller restart retains the same command identity and task lock.
	restarted, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r = restarted.Router()
	w = call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, w, 200)
	var again []control.Envelope
	json.Unmarshal(w.Body.Bytes(), &again)
	if len(again) != 1 || !bytes.Equal(again[0].Payload, envelopes[0].Payload) {
		t.Fatal("restart changed command")
	}
	result := control.Result{ID: task.ID, Status: "succeeded", Code: "VERIFIED", Image: image, Healthy: true}
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	// Generic configuration activation and rollback use the same approved path.
	for _, content := range []string{`{ "message": "alpha", "sequence": 9007199254740993 }`, `{"message":"beta"}`} {
		w = call(r, owner, "POST", "/api/v1/configs", map[string]any{"service": "worker", "kind": "json", "content": json.RawMessage(content)}, nil)
		check(t, w, 200)
		var config ConfigRelease
		json.Unmarshal(w.Body.Bytes(), &config)
		w = call(r, owner, "POST", "/api/v1/tasks", map[string]string{"service": "worker", "action": "configure", "config_version": config.ID}, nil)
		check(t, w, 200)
		var task Task
		json.Unmarshal(w.Body.Bytes(), &task)
		check(t, call(r, reviewer, "POST", "/api/v1/tasks/"+task.ID+"/approve", nil, nil), 200)
		check(t, call(r, nil, "GET", "/agent/v1/commands", nil, headers), 200)
		check(t, call(r, nil, "POST", "/agent/v1/results", control.Result{ID: task.ID, Status: "succeeded", Code: "VERIFIED", ConfigHash: config.SHA256, Healthy: true}, headers), 200)
	}
	for _, status := range []string{"queued", "dispatched"} {
		id := "expired-" + status
		row := Task{ID: id, HostID: "host-a", ServiceID: "worker", Action: "health", Status: status, CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
		if db.Create(&row).Error != nil || db.Model(&ServiceRecord{}).Where("id = ?", "worker").Update("busy_task", id).Error != nil {
			t.Fatal("expiry fixture")
		}
		if s.expireTasks(context.Background()) != nil {
			t.Fatal("expiration failed")
		}
		db.First(&row, "id = ?", id)
		var record ServiceRecord
		db.First(&record, "id = ?", "worker")
		if status == "queued" && (row.Status != "expired" || record.BusyTask != "") {
			t.Fatal("undelivered timeout did not release service")
		}
		if status == "dispatched" && (row.Status != "uncertain" || record.BusyTask != id) {
			t.Fatal("uncertain execution lost its lock")
		}
	}
	badCSRF := &browser{cookies: owner.cookies, csrf: "wrong"}
	check(t, call(r, badCSRF, "POST", "/api/v1/hosts", map[string]string{"id": "host-b"}, nil), 403)
	old := &browser{cookies: append([]*http.Cookie(nil), owner.cookies...), csrf: owner.csrf}
	check(t, call(r, owner, "POST", "/api/v1/logout", nil, nil), 200)
	check(t, call(r, old, "GET", "/api/v1/session", nil, nil), 401)
	var audit []Audit
	if db.Order("audit_id").Find(&audit).Error != nil || len(audit) < 8 {
		t.Fatal("audit missing")
	}
	previous := strings.Repeat("0", 64)
	for _, entry := range audit {
		if entry.PreviousHash != previous || entry.EntryHash != chainHash(previous, entry.OperatorID, entry.Action, entry.Target, entry.DetailJSON, entry.CreatedAtMS) {
			t.Fatal("audit chain invalid")
		}
		previous = entry.EntryHash
	}
	if err := Migrate(db); err != nil {
		t.Fatal("idempotent migration failed")
	}
	if db.Save(&SchemaVersion{ID: 1, Version: 6}).Error != nil {
		t.Fatal("version fixture")
	}
	if Migrate(db) == nil {
		t.Fatal("schema downgrade allowed")
	}
	db.Save(&SchemaVersion{ID: 1, Version: 5})
	if err = s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
