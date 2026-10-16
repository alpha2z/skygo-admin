package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/registrycache"
	"gorm.io/gorm"
)

func TestWorkflowPreparationUsesCentralDistributionMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	image := "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	def := control.Service{ID: "worker", HostID: "host", Image: image}
	raw, _ := json.Marshal(def)
	observation := control.Observation{Service: "worker", ConfiguredImage: image, ImageID: id, ScopeRevision: strings.Repeat("c", 64), Platform: "linux/amd64", Healthy: true, Running: true, Capabilities: []string{"image.archive.v1"}}
	observations, _ := json.Marshal([]control.Observation{observation})
	db.Create(&Host{ID: "host", Active: true, LastSeen: time.Now(), Observations: string(observations)})
	db.Create(&ServiceRecord{ID: "worker", Definition: string(raw)})
	plan := WorkflowPlan{Group: "fixture", Kind: "prepare", Title: "Prepare", Reason: "Synthetic verification", CacheOnly: true, Targets: []WorkflowTarget{{Service: "worker", Host: "host", Definition: string(raw), ScopeRevision: observation.ScopeRevision, Image: image, ImageID: id}}, Steps: []WorkflowStep{{Label: "Prepare", Command: control.Command{HostID: "host", Service: "worker", Action: "prepare-image", Image: image, Platform: "linux/amd64"}, ImageID: id}}}
	validations := 0
	cfg.Workflows = map[string]WorkflowProvider{"example": {Permission: "ops.write", ApprovalPermission: "ops.approve", Resolve: func(context.Context, *gorm.DB, string, json.RawMessage) (WorkflowPlan, error) { return plan, nil }, Validate: func(context.Context, *gorm.DB, WorkflowPlan) error { validations++; return nil }}}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	router := s.Router()
	password := "synthetic-distribution-passphrase"
	check(t, call(router, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, router, "owner", password)
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("synthetic"), 0600)
	s.distribution = &distributionConfig{Enabled: true, Directory: t.TempDir(), MaxBytes: 8 << 30, Username: "example", TokenFile: token, Packages: []string{"example/worker"}}
	s.registryClient = &registrycache.Client{Username: "example", TokenFile: token, Packages: []string{"example/worker"}, HTTP: &http.Client{Transport: registryTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic offline") })}}
	body := map[string]any{"request_id": "central-workflow", "provider": "example", "intent": map[string]string{"reason": "fixture"}, "preview_hash": planHash(plan)}
	check(t, call(router, owner, "POST", "/api/v1/workflows", body, nil), 200)
	if err := s.workflowTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var child Task
	if db.Where("workflow_id = ?", "central-workflow").First(&child).Error != nil {
		t.Fatal("child missing")
	}
	var delivery ImageDelivery
	if db.First(&delivery, "id = ?", child.DeliveryID).Error != nil || delivery.Mode != "archive" || child.Status != "preparing" {
		t.Fatal("workflow bypassed central queue")
	}
	if err := s.workflowTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var workflow Workflow
	db.First(&workflow, "id = ?", "central-workflow")
	if workflow.Status != "waiting" {
		t.Fatal("central download treated as failed")
	}
	before := validations
	s.distributionStep(context.Background())
	if validations <= before {
		t.Fatal("provider trust not revalidated")
	}
	db.First(&child, "id = ?", child.ID)
	if child.Status != "failed" {
		t.Fatalf("central failure must not fall back to direct pull: %s", child.Status)
	}
	if child.Result == "" || child.FinishedAt == nil {
		t.Fatal("central failure receipt missing")
	}
	if err := s.workflowTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.First(&workflow, "id = ?", "central-workflow")
	if workflow.Status != "blocked" {
		t.Fatal("failed preparation not blocked")
	}
	oldID := child.ID
	check(t, call(router, owner, "POST", "/api/v1/image-deliveries/"+delivery.ID+"/retry", nil, nil), 200)
	db.First(&workflow, "id = ?", "central-workflow")
	if workflow.ChildID == oldID || workflow.Status != "waiting" {
		t.Fatal("retry did not preserve workflow and attach new attempt")
	}
	var retried Task
	db.First(&retried, "id = ?", workflow.ChildID)
	if retried.WorkflowID != workflow.ID || retried.Status != "preparing" || retried.ExpiresAt.After(workflow.ExpiresAt) {
		t.Fatal("retry changed workflow authority or expiry")
	}
	var count int64
	db.Model(&Workflow{}).Count(&count)
	if count != 1 {
		t.Fatal("retry duplicated workflow")
	}

}
