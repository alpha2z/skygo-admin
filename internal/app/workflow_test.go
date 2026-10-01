package app

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkflowMigrationApprovalRecoveryMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	if db.Migrator().DropTable(&Workflow{}, &WorkflowLock{}) != nil || db.Migrator().DropColumn(&Task{}, "WorkflowID") != nil {
		t.Fatal("v4 fixture")
	}
	db.Save(&SchemaVersion{ID: 1, Version: 4})
	if db.Omit("WorkflowID").Create(&Task{ID: "retained", Status: "succeeded", CreatedAt: time.Now(), ExpiresAt: time.Now()}).Error != nil {
		t.Fatal("v4 task fixture")
	}
	if _, err := NewServer(cfg, db); err == nil {
		t.Fatal("implicit migration")
	}
	if Migrate(db) != nil {
		t.Fatal("v4 migration")
	}
	var retained Task
	if db.First(&retained, "id = ?", "retained").Error != nil {
		t.Fatal("old task lost")
	}
	plan := WorkflowPlan{Group: "fixture", Kind: "maintenance", Title: "Example", Reason: "synthetic verification"}
	cfg.Workflows = map[string]WorkflowProvider{"example": {Permission: "ops.write", ApprovalPermission: "ops.approve", Resolve: func(context.Context, *gorm.DB, string, json.RawMessage) (WorkflowPlan, error) { return plan, nil }}}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-workflow-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "reviewer", "Email": "reviewer@example.com", "Password": password, "Role": "superadmin"}, nil), 200)
	reviewer := signIn(t, s, r, "reviewer", password)
	check(t, call(r, owner, "POST", "/api/v1/hosts", map[string]string{"id": "host"}, nil), 200)
	image := "example/service@sha256:" + strings.Repeat("a", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	check(t, call(r, owner, "POST", "/api/v1/services", control.Service{ID: "service", HostID: "host", Image: image}, nil), 200)
	var record ServiceRecord
	db.First(&record, "id = ?", "service")
	obs := []control.Observation{{Service: "service", ImageID: id, ConfiguredImage: image, ScopeRevision: strings.Repeat("c", 64), Healthy: true, Running: true}}
	heartbeat := func() {
		b, _ := json.Marshal(obs)
		if db.Model(&Host{}).Where("id = ?", "host").Updates(map[string]any{"observations": string(b), "last_seen": time.Now()}).Error != nil {
			t.Fatal("heartbeat")
		}
	}
	heartbeat()
	plan.Targets = []WorkflowTarget{{Service: "service", Host: "host", Definition: record.Definition, ScopeRevision: obs[0].ScopeRevision, Image: image, ImageID: id}}
	plan.Steps = []WorkflowStep{{Label: "Check", Command: control.Command{HostID: "host", Service: "service", Action: "health"}}, {Label: "Stop", Command: control.Command{HostID: "host", Service: "service", Action: "stop"}, When: "running"}}
	body := map[string]any{"request_id": "workflow-one", "provider": "example", "intent": map[string]string{"reason": "fixture"}, "preview_hash": planHash(plan)}
	check(t, call(r, owner, "POST", "/api/v1/workflows/preview", body, nil), 200)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); check(t, call(r, owner, "POST", "/api/v1/workflows", body, nil), 200) }()
	}
	wg.Wait()
	var count int64
	db.Model(&Workflow{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate workflow")
	}
	check(t, call(r, owner, "POST", "/api/v1/workflows/workflow-one/approve", nil, nil), 409)
	check(t, call(r, reviewer, "POST", "/api/v1/workflows/workflow-one/approve", nil, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/services", control.Service{ID: "service", HostID: "host", Image: image}, nil), 409)
	if err = s.workflowTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var w Workflow
	db.First(&w, "id = ?", "workflow-one")
	original := w.ChildID
	if original == "" {
		t.Fatal("child missing")
	}
	restarted, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = restarted.workflowTick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&Task{}).Where("workflow_id = ?", w.ID).Count(&count)
	if count != 1 {
		t.Fatal("restart replay")
	}
	// A queued command must not survive config drift into dispatch.
	var child Task
	db.First(&child, "id = ?", original)
	obs[0].ScopeRevision = strings.Repeat("d", 64)
	heartbeat()
	if restarted.workflowDispatch(db, child) == nil {
		t.Fatal("scope drift accepted")
	}
	obs[0].ScopeRevision = plan.Targets[0].ScopeRevision
	heartbeat()
	if restarted.workflowDispatch(db, child) != nil {
		t.Fatal("unchanged scope rejected")
	}
	now := time.Now().Add(-10 * time.Second)
	result, _ := json.Marshal(control.Result{ID: child.ID, Status: "succeeded", Healthy: true})
	db.Model(&Task{}).Where("id = ?", child.ID).Updates(map[string]any{"status": "succeeded", "result": string(result), "finished_at": now})
	db.Model(&ServiceRecord{}).Where("id = ?", "service").Update("busy_task", "")
	heartbeat()
	if err = restarted.workflowTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.First(&w, "id = ?", w.ID)
	if w.Position != 1 || w.ChildID == original {
		t.Fatal("did not advance to exactly one next step")
	}
	db.Model(&Task{}).Where("id = ?", w.ChildID).Update("status", "uncertain")
	if err = restarted.workflowTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !workflowLocked(db, "service", "") {
		t.Fatal("uncertain scope unlocked")
	}
	// Request ID conflicts never mutate the original plan.
	body["intent"] = map[string]string{"reason": "different"}
	check(t, call(r, owner, "POST", "/api/v1/workflows", body, nil), 409)
}
func TestWorkflowRejectsExpandedAndMutatingCachePlans(t *testing.T) {
	p := WorkflowPlan{Group: "example", Reason: "fixture", CacheOnly: true, Targets: []WorkflowTarget{{Service: "service", Host: "host", Image: "example/service@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), ScopeRevision: strings.Repeat("c", 64)}}, Steps: []WorkflowStep{{Command: control.Command{HostID: "host", Service: "service", Action: "stop"}}}}
	if validWorkflowPlan(p) == nil {
		t.Fatal("cache mutation allowed")
	}
	p.CacheOnly = false
	p.Steps[0].Command.Service = "outside"
	if validWorkflowPlan(p) == nil {
		t.Fatal("scope expansion allowed")
	}
}

func TestWorkflowSignedTargetMustMatchReviewedHost(t *testing.T) {
	p := WorkflowPlan{Group: "example", Reason: "fixture", Targets: []WorkflowTarget{{Service: "service", Host: "host", Image: "example/service@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), ScopeRevision: strings.Repeat("c", 64)}}, Steps: []WorkflowStep{{Command: control.Command{HostID: "different", Service: "service", Action: "health"}}}}
	if validWorkflowPlan(p) == nil {
		t.Fatal("unreviewed host accepted")
	}
}

func TestPreparedIdentityOnlyAcceptsReviewedConfigOrManifest(t *testing.T) {
	ref := "example/service@sha256:" + strings.Repeat("a", 64)
	step := WorkflowStep{Command: control.Command{Image: ref, Platform: "linux/amd64"}, ImageID: "sha256:" + strings.Repeat("b", 64), ImageIDAlternatives: []string{"sha256:" + strings.Repeat("a", 64)}}
	result := control.Result{Image: ref, Platform: "linux/amd64", ImageID: step.ImageID}
	if !workflowPreparedIdentity(step, result) {
		t.Fatal("classic image store rejected")
	}
	result.ImageID = step.ImageIDAlternatives[0]
	if !workflowPreparedIdentity(step, result) {
		t.Fatal("manifest image store rejected")
	}
	result.ImageID = "sha256:" + strings.Repeat("c", 64)
	if workflowPreparedIdentity(step, result) {
		t.Fatal("unreviewed identity accepted")
	}
	result.ImageID = step.ImageID
	result.Platform = "linux/arm64"
	if workflowPreparedIdentity(step, result) {
		t.Fatal("other architecture accepted")
	}
}

func TestWorkflowAlternativeCannotWidenMutationScope(t *testing.T) {
	ref := "example/service@sha256:" + strings.Repeat("a", 64)
	p := WorkflowPlan{Group: "example", Reason: "fixture", CacheOnly: true, Targets: []WorkflowTarget{{Service: "service", Host: "host", Image: ref, ImageID: "sha256:" + strings.Repeat("b", 64), ScopeRevision: strings.Repeat("c", 64)}}, Steps: []WorkflowStep{{Command: control.Command{HostID: "host", Service: "service", Action: "prepare-image", Image: ref, Platform: "linux/amd64"}, ImageID: "sha256:" + strings.Repeat("b", 64), ImageIDAlternatives: []string{"sha256:" + strings.Repeat("a", 64)}}}}
	if validWorkflowPlan(p) != nil {
		t.Fatal("approved manifest alternative rejected")
	}
	p.Steps[0].ImageIDAlternatives[0] = "sha256:" + strings.Repeat("d", 64)
	if validWorkflowPlan(p) == nil {
		t.Fatal("arbitrary alternative accepted")
	}
	p.Steps[0].ImageIDAlternatives[0] = "sha256:" + strings.Repeat("a", 64)
	p.CacheOnly = false
	p.Steps[0].Command.Action = "deploy"
	p.Steps[0].Command.Platform = ""
	if validWorkflowPlan(p) == nil {
		t.Fatal("mutation did not freeze a single runtime identity")
	}
}
