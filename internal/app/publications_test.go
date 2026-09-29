package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/release"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicationMigrationApprovalRecoveryMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	// Recreate v2's physical schema, retaining an actual legacy task and receipt.
	if db.Migrator().DropTable(&Publication{}) != nil || db.Migrator().DropColumn(&Task{}, "PublicationID") != nil {
		t.Fatal("v2 fixture")
	}
	db.Save(&SchemaVersion{ID: 1, Version: 2})
	if db.Exec("INSERT INTO tasks (id, status, result) VALUES (?, ?, ?)", "legacy", "succeeded", `{"status":"succeeded"}`).Error != nil {
		t.Fatal("legacy fixture")
	}
	if _, err := NewServer(cfg, db); err == nil {
		t.Fatal("v2 accepted without migration")
	}
	if Migrate(db) != nil || !db.Migrator().HasColumn(&Task{}, "PublicationID") {
		t.Fatal("v2 migration")
	}
	var old Task
	if db.First(&old, "id = ?", "legacy").Error != nil || old.Result != `{"status":"succeeded"}` {
		t.Fatal("migration lost legacy result")
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-long-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "approver", "Email": "approver@example.com", "Password": password, "Role": "approver"}, nil), 200)
	approver := signIn(t, s, r, "approver", password)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	db.Create(&BuildTrustKey{ID: control.Digest(pub), PublicKey: base64.StdEncoding.EncodeToString(pub)})
	image := "example/api@sha256:" + strings.Repeat("b", 64)
	oldID := "sha256:" + strings.Repeat("a", 64)
	newID := "sha256:" + strings.Repeat("b", 64)
	m := release.Manifest{Version: 1, ID: "ci-1-1", Build: release.Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: strings.Repeat("a", 40), RunID: 1, RunAttempt: 1}, Images: []release.Image{{Service: "admin-api", Platform: "linux/arm64", Reference: image}}}
	signed, _ := release.Sign(m, key)
	raw, _ := json.Marshal(signed)
	db.Create(&ImageRelease{ID: m.ID, Payload: string(raw), CreatedAt: time.Now().UTC()})
	token := strings.Repeat("t", 40)
	headers := map[string]string{"X-Host-ID": "host", "Authorization": "Bearer " + token}
	if err := db.Create(&Host{ID: "host", TokenHash: control.Digest([]byte(token)), Active: true, LastSeen: time.Now().UTC()}).Error; err != nil {
		t.Fatal("host fixture failed", err)
	}
	observations := []control.Observation{}
	for _, id := range []string{"api", "web"} {
		def := control.Service{ID: id, HostID: "host", Image: "example/" + id + "@sha256:" + strings.Repeat("a", 64), ControlPlane: true, ControlKind: "admin-" + id, Platform: "linux/arm64"}
		b, _ := json.Marshal(def)
		db.Create(&ServiceRecord{ID: id, Definition: string(b)})
		observations = append(observations, control.Observation{Service: id, Image: def.Image, ImageID: oldID, Platform: def.Platform, Healthy: true, Running: true, UnitID: "admin", ControlKind: def.ControlKind, InventoryRevision: strings.Repeat("c", 64), Capabilities: []string{"image.prepare.v1", "control.unit.v1"}})
	}
	heartbeat := func() {
		check(t, call(r, nil, "POST", "/agent/v1/heartbeat", control.Heartbeat{Version: 1, BootID: "boot", Observations: observations}, headers), 200)
	}
	heartbeat()
	req := publicationRequest{ID: "publish-a", HostID: "host", ReleaseID: m.ID, Selected: []string{"api"}}
	check(t, call(r, owner, "POST", "/api/v1/publications", req, nil), 409)
	check(t, call(r, owner, "POST", "/api/v1/publication-preparation", req, nil), 200)
	commands := call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, commands, 200)
	var envelopes []control.Envelope
	json.Unmarshal(commands.Body.Bytes(), &envelopes)
	if len(envelopes) != 1 {
		t.Fatal("unselected companion prepared")
	}
	var prepare control.Command
	json.Unmarshal(envelopes[0].Payload, &prepare)
	check(t, call(r, nil, "POST", "/agent/v1/results", control.Result{ID: prepare.ID, Status: "succeeded", Image: image, ImageID: newID, Platform: "linux/arm64"}, headers), 200)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call(r, owner, "POST", "/api/v1/publications", req, nil).Code != 200 {
				t.Error("concurrent create")
			}
		}()
	}
	wg.Wait()
	conflict := req
	conflict.Selected = []string{"web"}
	check(t, call(r, owner, "POST", "/api/v1/publications", conflict, nil), 409)
	check(t, call(r, owner, "POST", "/api/v1/publications/publish-a/approve", nil, nil), 409)
	observations[0].InventoryRevision = strings.Repeat("d", 64)
	heartbeat()
	check(t, call(r, approver, "POST", "/api/v1/publications/publish-a/approve", nil, nil), 409)
	observations[0].InventoryRevision = strings.Repeat("c", 64)
	heartbeat()
	// Stale preparation fails approval, a fresh reinspection preserves the same scope.
	stale := time.Now().Add(-6 * time.Minute)
	db.Model(&Task{}).Where("id = ?", prepare.ID).Update("finished_at", stale)
	check(t, call(r, approver, "POST", "/api/v1/publications/publish-a/approve", nil, nil), 409)
	db.Model(&Task{}).Where("id = ?", prepare.ID).Update("finished_at", time.Now().UTC())
	check(t, call(r, approver, "POST", "/api/v1/publications/publish-a/approve", nil, nil), 200)
	var p Publication
	db.First(&p, "id = ?", req.ID)
	var locks int64
	db.Model(&ServiceRecord{}).Where("busy_task = ?", p.ExecutionID).Count(&locks)
	if locks != 2 {
		t.Fatal("both members not locked")
	}
	commands = call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, commands, 200)
	json.Unmarshal(commands.Body.Bytes(), &envelopes)
	if len(envelopes) != 1 {
		t.Fatal("missing unit")
	}
	var unit control.Command
	json.Unmarshal(envelopes[0].Payload, &unit)
	if unit.Unit == nil || unit.Unit.Targets[1].Image != oldID || unit.Unit.Targets[1].Selected {
		t.Fatal("companion was not pinned")
	}
	// Recreated controller must replay the exact command after its own service changes.
	observations[0].ImageID = newID
	heartbeat()
	fresh, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r = fresh.Router()
	replay := call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, replay, 200)
	var repeated []control.Envelope
	json.Unmarshal(replay.Body.Bytes(), &repeated)
	if len(repeated) != 1 || string(repeated[0].Payload) != string(envelopes[0].Payload) {
		t.Fatal("recovery expanded scope")
	}
	db.Model(&Task{}).Where("id = ?", p.ExecutionID).Update("expires_at", time.Now().Add(-time.Minute))
	if fresh.expireTasks(context.Background()) != nil {
		t.Fatal("expiry")
	}
	db.Model(&ServiceRecord{}).Where("busy_task = ?", p.ExecutionID).Count(&locks)
	if locks != 2 {
		t.Fatal("uncertain unit released locks")
	}
	result := control.Result{ID: p.ExecutionID, Status: "succeeded", Code: "UNIT_VERIFIED", Unit: []control.UnitProof{{Service: "api", ImageID: newID, Healthy: true}, {Service: "web", ImageID: oldID, Healthy: true}}}
	invalid := result
	invalid.Unit = result.Unit[:1]
	check(t, call(r, nil, "POST", "/agent/v1/results", invalid, headers), 409)
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	check(t, call(r, nil, "POST", "/agent/v1/results", result, headers), 200)
	db.Model(&ServiceRecord{}).Where("busy_task = ?", p.ExecutionID).Count(&locks)
	if locks != 0 {
		t.Fatal("verified unit did not release locks")
	}
	check(t, call(r, owner, "POST", "/api/v1/publications", req, nil), 200)
	var count int64
	db.Model(&Task{}).Where("publication_id = ?", p.ID).Count(&count)
	if count != 1 {
		t.Fatal("duplicate execution task")
	}
	check(t, call(r, owner, "GET", "/api/v1/publication-candidates?release_id="+m.ID, nil, nil), 200)
	// New request for the already running target is rejected even with a fresh receipt.
	req.ID = "no-change"
	check(t, call(r, owner, "POST", "/api/v1/publications", req, nil), 409)
	// A queued command whose frozen configuration changed is never dispatched.
	observations[0].ImageID = oldID
	heartbeat()
	req.ID = "dispatch-drift"
	check(t, call(r, owner, "POST", "/api/v1/publications", req, nil), 200)
	check(t, call(r, approver, "POST", "/api/v1/publications/dispatch-drift/approve", nil, nil), 200)
	observations[0].InventoryRevision = strings.Repeat("e", 64)
	heartbeat()
	rejected := call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, rejected, 200)
	var none []control.Envelope
	json.Unmarshal(rejected.Body.Bytes(), &none)
	if len(none) != 0 {
		t.Fatal("changed scope dispatched")
	}
	var drift Publication
	db.First(&drift, "id = ?", req.ID)
	if drift.Status != "failed" || drift.Code != "SCOPE_CHANGED" {
		t.Fatal("dispatch rejection not persisted")
	}
	db.Model(&ServiceRecord{}).Where("busy_task <> ?", "").Count(&locks)
	if locks != 0 {
		t.Fatal("undelivered unit kept locks")
	}
	observations[0].InventoryRevision = strings.Repeat("c", 64)
	heartbeat()
	req.ID = "reject-me"
	check(t, call(r, owner, "POST", "/api/v1/publications", req, nil), 200)
	check(t, call(r, approver, "POST", "/api/v1/publications/reject-me/reject", nil, nil), 200)
	check(t, call(r, approver, "POST", "/api/v1/publications/reject-me/approve", nil, nil), 409)
	req.ID = "expired-pending"
	check(t, call(r, owner, "POST", "/api/v1/publications", req, nil), 200)
	db.Model(&Publication{}).Where("id = ?", req.ID).Update("expires_at", time.Now().Add(-time.Minute))
	if fresh.expireTasks(context.Background()) != nil {
		t.Fatal("pending expiry")
	}
	var expired Publication
	db.First(&expired, "id = ?", req.ID)
	if expired.Status != "expired" {
		t.Fatal("pending publication never expired")
	}
	invalidFields := map[string]any{"request_id": "untrusted-payload", "host_id": "host", "release_id": m.ID, "selected": []string{"api"}, "image": image}
	check(t, call(r, owner, "POST", "/api/v1/publications", invalidFields, nil), 400)
	for _, asset := range []string{"/publications.js", "/app.js", "/styles.css"} {
		check(t, call(r, nil, "GET", asset, nil, nil), 200)
	}
	check(t, call(r, nil, "GET", "/missing-module.js", nil, nil), 404)
}
