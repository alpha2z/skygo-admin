package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/release"
)

func TestAgentUpdateCentralPreparationAndHeartbeatMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	cfg.IndependentApprovalEnabled = false
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-update-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	centralTestConfig(t, s)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	db.Create(&BuildTrustKey{ID: control.Digest(pub), PublicKey: base64.StdEncoding.EncodeToString(pub)})
	ref := "ghcr.io/example/ops-agent@sha256:" + strings.Repeat("b", 64)
	old := "sha256:" + strings.Repeat("a", 64)
	next := "sha256:" + strings.Repeat("c", 64)
	m := release.Manifest{Version: 1, ID: "ci-1-1", Build: release.Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: strings.Repeat("a", 40), RunID: 1, RunAttempt: 1}, Images: []release.Image{{Service: "ops-agent", Platform: "linux/amd64", Reference: ref}}}
	signed, _ := release.Sign(m, key)
	raw, _ := json.Marshal(signed)
	db.Create(&ImageRelease{ID: m.ID, Payload: string(raw)})
	def := control.Service{ID: "agent", HostID: "host", Image: ref, ControlPlane: true, ControlKind: "ops-agent", Platform: "linux/amd64"}
	raw, _ = json.Marshal(def)
	db.Create(&ServiceRecord{ID: def.ID, Definition: string(raw)})
	token := strings.Repeat("t", 40)
	db.Create(&Host{ID: "host", TokenHash: control.Digest([]byte(token)), Active: true, LastSeen: time.Now().UTC()})
	headers := map[string]string{"X-Host-ID": "host", "Authorization": "Bearer " + token}
	observation := control.Observation{Service: "agent", Image: ref, ImageID: old, Platform: "linux/amd64", Running: true, Healthy: true, SyncImageEnv: true, InventoryRevision: strings.Repeat("d", 64), Capabilities: []string{"agent.self-update.v1", "image.archive.v1", "image.env.v1"}}
	heartbeat := func(boot string) {
		check(t, call(r, nil, "POST", "/agent/v1/heartbeat", control.Heartbeat{Version: 1, BootID: boot, Observations: []control.Observation{observation}}, headers), 200)
	}
	heartbeat("boot-old")
	req := map[string]string{"request_id": "prepare-agent", "service": "agent", "release_id": m.ID}
	s.distribution = nil
	check(t, call(r, owner, "POST", "/api/v1/agent-updates/prepare", req, nil), 409)
	centralTestConfig(t, s)
	check(t, call(r, owner, "POST", "/api/v1/agent-updates/prepare", req, nil), 200)
	centralTestArchives(t, s, next)
	check(t, call(r, nil, "GET", "/agent/v1/commands", nil, headers), 200)
	bad := control.Result{ID: "prepare-agent", Status: "succeeded", Image: ref, ImageID: old, Platform: "linux/amd64"}
	check(t, call(r, nil, "POST", "/agent/v1/results", bad, headers), 409)
	bad.ImageID = next
	check(t, call(r, nil, "POST", "/agent/v1/results", bad, headers), 200)
	req["request_id"] = "update-agent"
	req["preparation_id"] = "prepare-agent"
	check(t, call(r, owner, "POST", "/api/v1/agent-updates", req, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/agent-updates", req, nil), 200)
	if !updateHostBlocked(db, "host", "") {
		t.Fatal("host gate absent")
	}
	check(t, call(r, nil, "GET", "/agent/v1/commands", nil, headers), 200)
	result := control.Result{ID: "update-agent", Status: "succeeded", Image: ref, ImageID: next, Platform: "linux/amd64", Healthy: true}
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 409)
	observation.ImageID = next
	heartbeat("boot-old")
	check(t, call(r, nil, "GET", "/agent/v1/self-update/update-agent/confirmation", nil, headers), 409)
	heartbeat("boot-new")
	check(t, call(r, nil, "GET", "/agent/v1/self-update/update-agent/confirmation", nil, headers), 200)
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	if updateHostBlocked(db, "host", "") {
		t.Fatal("host gate retained after confirmed success")
	}
	// Old queued pulls are failed durably, never translated into Registry access.
	c := control.Command{Version: 1, ID: "legacy-pull", HostID: "host", Service: "agent", Action: "prepare-image", Image: ref, Platform: "linux/amd64", ExpiresAt: time.Now().Add(time.Minute)}
	raw, _ = json.Marshal(c)
	db.Create(&Task{ID: c.ID, HostID: c.HostID, ServiceID: c.Service, Action: c.Action, Payload: string(raw), Status: "queued", ExpiresAt: c.ExpiresAt, RequestedBy: 1, SingleConfirmation: true})
	check(t, call(r, nil, "GET", "/agent/v1/commands", nil, headers), 200)
	var legacy Task
	db.First(&legacy, "id = ?", c.ID)
	if legacy.Status != "failed" {
		t.Fatal("legacy direct task dispatched")
	}
}
func TestCentralPolicyRejectsUnconfiguredImages(t *testing.T) {
	s := &Server{}
	for _, action := range []string{"prepare-image", "deploy", "rollback", "control-unit", "upgrade-agent"} {
		if s.centralPolicy(nil, control.Command{Action: action}) == nil {
			t.Fatal(action)
		}
	}
	if s.centralPolicy(nil, control.Command{Action: "health"}) != nil {
		t.Fatal("unconfigured Admin must remain manageable")
	}
	if err := s.queuePreparation(nil, &Task{}, "", Host{}).Error; err == nil {
		t.Fatal("preparation bypassed configuration")
	}
}

func TestCentralPolicyPreservesFrozenLegacyRollbackMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	centralTestConfig(t, s)
	image := "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)
	plan := WorkflowPlan{Targets: []WorkflowTarget{{Host: "host", Service: "worker", Image: image, ImageID: "sha256:" + strings.Repeat("b", 64)}}}
	raw, _ := json.Marshal(plan)
	now := time.Now().UTC()
	w := Workflow{ID: "recover", RollingBack: true, Plan: string(raw), ExpiresAt: now.Add(time.Hour)}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	c := control.Command{Action: "rollback", HostID: "host", Service: "worker", Image: image, WorkflowID: w.ID}
	if err := s.centralPolicy(db, c); err != nil {
		t.Fatal("frozen rollback blocked", err)
	}
	c.Image = "ghcr.io/example/worker@sha256:" + strings.Repeat("c", 64)
	if s.centralPolicy(db, c) == nil {
		t.Fatal("unreviewed rollback image accepted")
	}
	c.Image = image
	c.WorkflowID = ""
	if s.centralPolicy(db, c) == nil {
		t.Fatal("ordinary rollback bypassed central preparation")
	}
}
