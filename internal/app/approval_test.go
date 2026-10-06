package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"gorm.io/gorm"
)

func TestIndependentApprovalConfig(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	for name, value := range map[string]string{"ADMIN_MYSQL_DSN_FILE": "synthetic", "ADMIN_JWT_SECRET_FILE": strings.Repeat("j", 40), "ADMIN_BOOTSTRAP_TOKEN_FILE": strings.Repeat("b", 40), "ADMIN_SIGNING_KEY_FILE": base64.StdEncoding.EncodeToString(key)} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
	t.Setenv("ADMIN_EMAIL_CONFIRMATION_ENABLED", "false")
	for _, value := range []string{"", "false", "true", "yes"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("ADMIN_INDEPENDENT_APPROVAL_ENABLED", value)
			cfg, err := LoadConfig()
			if value == "yes" {
				if err == nil {
					t.Fatal("invalid setting accepted")
				}
				return
			}
			if err != nil || cfg.IndependentApprovalEnabled != (value == "true") {
				t.Fatalf("configuration: enabled=%v err=%v", cfg.IndependentApprovalEnabled, err)
			}
		})
	}
}

func TestSingleConfirmationTaskAndPolicySwitchMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	cfg.IndependentApprovalEnabled = false
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-approval-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	for _, role := range []string{"operator", "viewer"} {
		check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": role, "Email": role + "@example.com", "Password": password, "Role": role}, nil), 200)
	}
	operator := signIn(t, s, r, "operator", password)
	viewer := signIn(t, s, r, "viewer", password)
	check(t, call(r, owner, "POST", "/api/v1/hosts", map[string]string{"id": "host"}, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/services", control.Service{ID: "service", HostID: "host", Image: "example/service@sha256:" + strings.Repeat("a", 64)}, nil), 200)
	if err := db.Model(&Host{}).Where("id = ?", "host").Updates(map[string]any{"observations": "[]", "last_seen": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"request_id": "single-task", "service": "service", "action": "health"}
	check(t, call(r, viewer, "POST", "/api/v1/tasks", body, nil), 403)
	result := call(r, operator, "POST", "/api/v1/tasks", body, nil)
	check(t, result, 200)
	var task Task
	json.Unmarshal(result.Body.Bytes(), &task)
	if task.Status != "queued" || !task.SingleConfirmation || task.ApprovedBy != task.RequestedBy {
		t.Fatalf("not authorized once: %+v", task)
	}
	check(t, call(r, operator, "POST", "/api/v1/tasks", body, nil), 200)
	check(t, call(r, operator, "POST", "/api/v1/tasks/single-task/cancel", nil, nil), 409)
	// Restore dual mode without changing the queued record's authorization.
	cfg.IndependentApprovalEnabled = true
	s, err = NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r = s.Router()
	if err = s.taskAuthority(db, task); err != nil {
		t.Fatal("single mode acquired an approval permission requirement", err)
	}
	body["request_id"] = "dual-task"
	check(t, call(r, owner, "POST", "/api/v1/tasks", body, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/tasks/dual-task/approve", nil, nil), 409)
	cfg.IndependentApprovalEnabled = false
	s, err = NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r = s.Router()
	check(t, call(r, owner, "POST", "/api/v1/tasks/dual-task/approve", nil, nil), 409)
	check(t, call(r, operator, "POST", "/api/v1/tasks/dual-task/cancel", nil, nil), 409)
	check(t, call(r, owner, "POST", "/api/v1/tasks/dual-task/cancel", nil, nil), 200)
	if err = db.Model(&AdminUser{}).Where("id = ?", task.RequestedBy).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	if s.taskAuthority(db, task) == nil {
		t.Fatal("disabled requester retained execution authority")
	}
}

func TestSingleConfirmationWorkflowRecoveryMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	cfg.IndependentApprovalEnabled = false
	image := "example/service@sha256:" + strings.Repeat("a", 64)
	imageID := "sha256:" + strings.Repeat("b", 64)
	def := control.Service{ID: "service", HostID: "host", Image: image}
	raw, _ := json.Marshal(def)
	obs := []control.Observation{{Service: "service", ImageID: imageID, ConfiguredImage: image, ScopeRevision: strings.Repeat("c", 64), Running: true, Healthy: true}}
	observed, _ := json.Marshal(obs)
	db.Create(&Host{ID: "host", Active: true, LastSeen: time.Now(), Observations: string(observed)})
	db.Create(&ServiceRecord{ID: "service", Definition: string(raw)})
	plan := WorkflowPlan{Group: "group", Kind: "maintenance", Title: "Check", Reason: "fixture", Targets: []WorkflowTarget{{Service: "service", Host: "host", Definition: string(raw), ScopeRevision: obs[0].ScopeRevision, Image: image, ImageID: imageID}}, Steps: []WorkflowStep{{Label: "Health", Command: control.Command{HostID: "host", Service: "service", Action: "health"}}}}
	cfg.Workflows = map[string]WorkflowProvider{"fixture": {Permission: "ops.write", ApprovalPermission: "ops.approve", Resolve: func(context.Context, *gorm.DB, string, json.RawMessage) (WorkflowPlan, error) { return plan, nil }}}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-workflow-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "operator", "Email": "operator@example.com", "Password": password, "Role": "operator"}, nil), 200)
	operator := signIn(t, s, r, "operator", password)
	body := map[string]any{"request_id": "single-workflow", "provider": "fixture", "intent": map[string]string{"reason": "fixture"}, "preview_hash": planHash(plan)}
	check(t, call(r, operator, "POST", "/api/v1/workflows", body, nil), 200)
	var w Workflow
	db.First(&w, "id = ?", "single-workflow")
	if w.Status != "running" || !w.SingleConfirmation {
		t.Fatalf("workflow not executing: %+v", w)
	}
	cfg.IndependentApprovalEnabled = true
	s, err = NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.workflowTick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var tasks []Task
	db.Where("workflow_id = ?", w.ID).Find(&tasks)
	if len(tasks) != 1 || !tasks[0].SingleConfirmation {
		t.Fatal("recovery did not preserve a single authorized child")
	}
	if err = s.workflowDispatch(db, tasks[0]); err != nil {
		t.Fatal(err)
	}
	obs[0].ScopeRevision = strings.Repeat("d", 64)
	observed, _ = json.Marshal(obs)
	db.Model(&Host{}).Where("id = ?", "host").Update("observations", string(observed))
	if s.workflowDispatch(db, tasks[0]) == nil {
		t.Fatal("changed scope accepted")
	}
}

func TestApprovalMigrationRetainsLegacyPolicyMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	if err := db.Migrator().DropColumn(&Task{}, "SingleConfirmation"); err != nil {
		t.Fatal(err)
	}
	db.Save(&SchemaVersion{ID: 1, Version: 5})
	if err := db.Exec("INSERT INTO tasks (id,status,result) VALUES (?,?,?)", "legacy-pending", "pending", "retained").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(cfg, db); err == nil {
		t.Fatal("startup migrated implicitly")
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var task Task
	if err := db.First(&task, "id = ?", "legacy-pending").Error; err != nil {
		t.Fatal(err)
	}
	if task.SingleConfirmation || task.Status != "pending" || task.Result != "retained" {
		t.Fatal("legacy record changed")
	}
	if err := Migrate(db); err != nil {
		t.Fatal("migration not idempotent", err)
	}
}
