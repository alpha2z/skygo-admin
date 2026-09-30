package app

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

func TestExtensionRegistrationFailsClosed(t *testing.T) {
	good := Extension{ID: "sample", Policies: []RolePolicy{{Role: "superadmin", Permission: "sample.read"}}, Routes: []ExtensionRoute{{Method: "GET", Path: "/items", Permission: "sample.read", Handler: func(*gin.Context) {}}}}
	if validateExtensions([]Extension{good}) != nil {
		t.Fatal("valid extension rejected")
	}
	cases := []Extension{good, good, good, good}
	cases[0].ID = "../api"
	cases[1].Routes = []ExtensionRoute{{Method: "POST", Path: "/items", Permission: "missing", Handler: func(*gin.Context) {}}}
	cases[2].PublicRoutes = []ExtensionRoute{{Method: "GET", Path: "/api/bootstrap", Handler: func(*gin.Context) {}}}
	cases[3].Pages = []ExtensionPage{{ID: "sample", Title: "Sample", Permission: "sample.read", Script: "../app.js"}}
	for i, x := range cases {
		if validateExtensions([]Extension{x}) == nil {
			t.Fatalf("invalid extension %d accepted", i)
		}
	}
	if validateExtensions([]Extension{good, good}) == nil {
		t.Fatal("duplicate namespace accepted")
	}
}
func TestExtensionRoutesAndAssetsMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	var calls atomic.Int32
	cfg.Extensions = []Extension{{ID: "sample", Policies: []RolePolicy{{Role: "superadmin", Permission: "sample.read"}, {Role: "superadmin", Permission: "sample.write"}}, Routes: []ExtensionRoute{
		{Method: "GET", Path: "/items", Permission: "sample.read", Handler: func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) }},
		{Method: "POST", Path: "/items", Permission: "sample.write", Handler: func(c *gin.Context) { calls.Add(1); c.JSON(201, gin.H{"actor": c.GetUint("admin_id")}) }},
	}, Assets: fstest.MapFS{"sample.js": &fstest.MapFile{Data: []byte("export function render(){}")}}, Pages: []ExtensionPage{{ID: "sample", Title: "Sample", Permission: "sample.read", Script: "sample.js"}}}}
	if ExtensionPolicies(db, cfg.Extensions) != nil {
		t.Fatal("extension migration")
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	if w := call(r, nil, "GET", "/app.js", nil, nil); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("UI script can be cached across rollback")
	}
	check(t, call(r, nil, "GET", "/api/v1/extensions/sample/items", nil, nil), 401)
	check(t, call(r, nil, "GET", "/extensions/sample/assets/missing.js", nil, nil), 401)
	password := "synthetic-extension-password"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	if w := call(r, owner, "GET", "/extensions/sample/assets/sample.js", nil, nil); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("extension asset can be cached across rollback")
	}

	check(t, call(r, owner, "GET", "/extensions/sample/assets/missing.js", nil, nil), 404)
	check(t, call(r, owner, "GET", "/api/v1/extensions", nil, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/extensions/sample/items", map[string]string{"actor": "forged"}, nil), 201)
	check(t, call(r, owner, "POST", "/api/v1/extensions/sample/items", nil, map[string]string{"X-CSRF-Token": "wrong"}), 403)
	if calls.Load() != 1 {
		t.Fatal("unauthorized handler invocation")
	}
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "viewer", "Email": "viewer@example.com", "Password": password, "Role": "viewer"}, nil), 200)
	viewer := signIn(t, s, r, "viewer", password)
	check(t, call(r, viewer, "GET", "/api/v1/extensions/sample/items", nil, nil), 403)
	var pages []ExtensionPage
	w := call(r, viewer, "GET", "/api/v1/extensions", nil, nil)
	check(t, w, 200)
	if json.Unmarshal(w.Body.Bytes(), &pages) != nil || len(pages) != 0 {
		t.Fatal("unauthorized page exposed")
	}
	cfg.Extensions[0].Start = func(context.Context) error { return errors.New("private failure") }
	s.cfg = cfg
	if s.Start(context.Background()) == nil {
		t.Fatal("failed extension started")
	}
	_ = http.MethodGet
}

func TestStreamingDraftCannotBeDeclaredAsPublicMutation(t *testing.T) {
	ext := Extension{ID: "upload", Policies: []RolePolicy{{Role: "operator", Permission: "upload.write"}}, Routes: []ExtensionRoute{{Method: "POST", Path: "/draft", Permission: "upload.write", Draft: true, BodyLimit: 4 << 30, Handler: func(*gin.Context) {}}}}
	if validateExtensions([]Extension{ext}) != nil {
		t.Fatal("bounded streaming draft rejected")
	}
	ext.Routes[0].Draft = false
	if validateExtensions([]Extension{ext}) == nil {
		t.Fatal("unbounded confirmed mutation accepted")
	}
	ext.Routes[0].Draft = true
	ext.Routes[0].Method = "GET"
	if validateExtensions([]Extension{ext}) == nil {
		t.Fatal("GET draft mutation accepted")
	}
}

func TestExtensionTaskAuthorizationAndDispatchMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	var blocked atomic.Bool
	cfg.TaskPolicy = func(_ context.Context, _ *gorm.DB, _ control.Command) error {
		if blocked.Load() {
			return errors.New("policy changed")
		}
		return nil
	}
	cfg.Extensions = []Extension{{ID: "sample", Policies: []RolePolicy{{Role: "superadmin", Permission: "sample.execute"}, {Role: "approver", Permission: "sample.approve"}}}}
	cfg.AgentActions = map[string]AgentAction{"sample": {Permission: "sample.execute", ApprovalPermission: "sample.approve", Validate: func(b json.RawMessage) error {
		if string(b) != `{"step":1}` {
			return errors.New("invalid scope")
		}
		return nil
	}}}
	if err := ExtensionPolicies(db, cfg.Extensions); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-extension-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "reviewer", "Email": "reviewer@example.com", "Password": password, "Role": "approver"}, nil), 200)
	reviewer := signIn(t, s, r, "reviewer", password)
	w := call(r, owner, "POST", "/api/v1/hosts", map[string]string{"id": "sample-host"}, nil)
	check(t, w, 200)
	var enrollment struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &enrollment)
	headers := map[string]string{"X-Host-ID": "sample-host", "Authorization": "Bearer " + enrollment.Token}
	check(t, call(r, owner, "POST", "/api/v1/services", control.Service{ID: "sample", HostID: "sample-host", Image: "example/sample@sha256:" + strings.Repeat("a", 64)}, nil), 200)
	heartbeat := func(capability bool) {
		o := control.Observation{Service: "sample"}
		if capability {
			o.Capabilities = []string{"extension.sample.v1"}
		}
		check(t, call(r, nil, "POST", "/agent/v1/heartbeat", control.Heartbeat{Version: 1, BootID: "sample-boot", Observations: []control.Observation{o}}, headers), 200)
	}
	request := map[string]any{"request_id": "extension-1", "service": "sample", "action": "extension", "extension": "sample", "payload": json.RawMessage(`{"step":1}`)}
	heartbeat(false)
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 409)
	heartbeat(true)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks", request, nil), 403)
	blocked.Store(true)
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 409)
	blocked.Store(false)
	w = call(r, owner, "POST", "/api/v1/tasks", request, nil)
	check(t, w, 200)
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/tasks/extension-1/approve", nil, nil), 409)
	heartbeat(false)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks/extension-1/approve", nil, nil), 409)
	heartbeat(true)
	blocked.Store(true)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks/extension-1/approve", nil, nil), 409)
	blocked.Store(false)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks/extension-1/approve", nil, nil), 200)
	blocked.Store(true)
	w = call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, w, 200)
	var commands []control.Envelope
	json.Unmarshal(w.Body.Bytes(), &commands)
	if len(commands) != 0 {
		t.Fatal("changed policy dispatched")
	}
	var failed Task
	db.First(&failed, "id = ?", "extension-1")
	if failed.Status != "failed" {
		t.Fatal("undispatched task not terminated")
	}
	blocked.Store(false)
	request["request_id"] = "extension-2"
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 200)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks/extension-2/approve", nil, nil), 200)
	// Revoked approver permissions must be checked again immediately before dispatch.
	s.auth.RemovePolicy("approver", "sample.approve")
	w = call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &commands)
	if len(commands) != 0 {
		t.Fatal("revoked permission dispatched")
	}
	var revoked Task
	db.First(&revoked, "id = ?", "extension-2")
	if revoked.Status != "failed" {
		t.Fatal("revoked queued task remains executable")
	}
	s.auth.AddPolicy("approver", "sample.approve")
	request["request_id"] = "extension-3"
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 200)
	check(t, call(r, reviewer, "POST", "/api/v1/tasks/extension-3/approve", nil, nil), 200)
	w = call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &commands)
	if len(commands) != 1 {
		t.Fatal("signed extension command absent")
	}
	cmd, err := control.Verify(commands[0], cfg.SigningKey.Public().(ed25519.PublicKey), "sample-host")
	if err != nil || cmd.ID != "extension-3" || cmd.Extension != "sample" {
		t.Fatal("wrong execution scope")
	}
	result := control.Result{ID: cmd.ID, Status: "succeeded", Code: "SAMPLE_DONE", Data: json.RawMessage(`{"verified":true}`)}
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	var count int64
	db.Model(&Task{}).Count(&count)
	if count != 3 {
		t.Fatal("duplicate request created a task")
	}
}
