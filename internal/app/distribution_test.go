package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/registrycache"
	"github.com/alpha2z/skygo-admin/internal/release"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type registryTransport func(*http.Request) (*http.Response, error)

func (f registryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDistributionMigrationAttemptsAndCleanupMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	if db.Migrator().DropTable(&ImageDelivery{}, &ImageAttempt{}, &ImageCache{}, &ImageCleanup{}) != nil || db.Migrator().DropColumn(&Task{}, "DeliveryID") != nil || db.Migrator().DropColumn(&ImageRelease{}, "BuildStartedAt") != nil {
		t.Fatal("v3 physical fixture")
	}
	db.Save(&SchemaVersion{ID: 1, Version: 3})
	db.Exec("INSERT INTO tasks (id,status,result) VALUES (?,?,?)", "legacy", "succeeded", `{"status":"succeeded"}`)
	if _, err := NewServer(cfg, db); err == nil {
		t.Fatal("v3 startup bypassed migration")
	}
	if Migrate(db) != nil {
		t.Fatal("migration failed")
	}
	var old Task
	if db.First(&old, "id = ?", "legacy").Error != nil || old.Result != `{"status":"succeeded"}` {
		t.Fatal("legacy receipt lost")
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-long-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/admins", map[string]string{"Username": "reviewer", "Email": "reviewer@example.com", "Password": password, "Role": "approver"}, nil), 200)
	reviewer := signIn(t, s, r, "reviewer", password)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	db.Create(&BuildTrustKey{ID: control.Digest(pub), PublicKey: base64.StdEncoding.EncodeToString(pub)})
	config := []byte(`{"os":"linux","architecture":"arm64","rootfs":{"type":"layers","diff_ids":[]}}`)
	imageID := "sha256:" + control.Digest(config)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]any{"digest": imageID, "size": len(config)}, "layers": []any{}})
	reference := "ghcr.io/example/admin-api@sha256:" + control.Digest(manifest)
	for n := 1; n <= 6; n++ {
		ref := reference
		if n > 1 {
			ref = "ghcr.io/example/admin-api@sha256:" + strings.Repeat(fmt.Sprint(n), 64)
		}
		m := release.Manifest{Version: 1, ID: fmt.Sprintf("ci-%d-1", n), Build: release.Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: strings.Repeat("a", 40), RunID: int64(n), RunAttempt: 1}, Images: []release.Image{{Service: "admin-api", Platform: "linux/arm64", Reference: ref}}}
		signed, _ := release.Sign(m, key)
		raw, _ := json.Marshal(signed)
		db.Create(&ImageRelease{ID: m.ID, Payload: string(raw), CreatedAt: time.Now().Add(time.Duration(n-20) * 24 * time.Hour)})
	}
	token := strings.Repeat("t", 40)
	headers := map[string]string{"X-Host-ID": "host", "Authorization": "Bearer " + token}
	db.Create(&Host{ID: "host", TokenHash: control.Digest([]byte(token)), Active: true, LastSeen: time.Now()})
	observations := []control.Observation{}
	for _, id := range []string{"api", "web"} {
		def := control.Service{ID: id, HostID: "host", Image: "example/" + id + "@sha256:" + strings.Repeat("f", 64), ControlPlane: true, ControlKind: "admin-" + id, Platform: "linux/arm64"}
		b, _ := json.Marshal(def)
		db.Create(&ServiceRecord{ID: id, Definition: string(b)})
		observations = append(observations, control.Observation{Service: id, Image: def.Image, ImageID: "sha256:" + strings.Repeat("f", 64), Platform: def.Platform, Healthy: true, Running: true, UnitID: "admin", ControlKind: def.ControlKind, InventoryRevision: strings.Repeat("c", 64), Capabilities: []string{"image.prepare.v1", "control.unit.v1", "image.archive.v1", "image.cleanup.v1"}})
	}
	heartbeat := func() {
		check(t, call(r, nil, "POST", "/agent/v1/heartbeat", control.Heartbeat{Version: 1, BootID: "boot", Observations: observations}, headers), 200)
	}
	heartbeat()
	directory := t.TempDir()
	tokenPath := filepath.Join(t.TempDir(), "registry-token")
	os.WriteFile(tokenPath, []byte("synthetic-provider-value"), 0600)
	s.distribution = &distributionConfig{Enabled: true, Directory: directory, MaxBytes: 64 << 30, Packages: []string{"example/admin-api"}, Username: "example", TokenFile: tokenPath}
	s.registryClient = &registrycache.Client{Username: "example", TokenFile: tokenPath, Packages: []string{"example/admin-api"}, HTTP: &http.Client{Transport: registryTransport(func(req *http.Request) (*http.Response, error) {
		body := manifest
		if req.URL.Path == "/token" {
			body = []byte(`{"token":"synthetic-scoped-value"}`)
		} else if strings.HasSuffix(req.URL.Path, imageID) {
			body = config
		}
		return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	req := publicationRequest{ID: "prepare", HostID: "host", ReleaseID: "ci-1-1", Selected: []string{"api"}}
	check(t, call(r, owner, "POST", "/api/v1/publication-preparation", req, nil), 200)
	var d ImageDelivery
	if db.First(&d).Error != nil || d.Status != "preparing" {
		t.Fatal("archive mode not queued")
	}
	check(t, call(r, nil, "GET", "/agent/v1/image-deliveries/"+d.ID+"/archive", nil, headers), 403)
	s.distributionStep(context.Background())
	db.First(&d, "id = ?", d.ID)
	if d.Status != "queued" {
		t.Fatalf("central preparation failed: %s", d.Status)
	}
	commands := call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, commands, 200)
	var envs []control.Envelope
	json.Unmarshal(commands.Body.Bytes(), &envs)
	if len(envs) != 1 {
		t.Fatal("missing archive command")
	}
	var command control.Command
	json.Unmarshal(envs[0].Payload, &command)
	if command.Archive == nil || command.Archive.ImageID != imageID {
		t.Fatal("archive identity missing")
	}
	check(t, call(r, nil, "GET", "/agent/v1/image-deliveries/"+d.ID+"/archive", nil, headers), 200)
	rangeHeaders := map[string]string{"X-Host-ID": "host", "Authorization": "Bearer " + token, "Range": "bytes=64-"}
	check(t, call(r, nil, "GET", "/agent/v1/image-deliveries/"+d.ID+"/archive", nil, rangeHeaders), 206)
	db.Create(&Host{ID: "other", TokenHash: control.Digest([]byte(token)), Active: true, LastSeen: time.Now(), Observations: "[]"})
	check(t, call(r, nil, "GET", "/agent/v1/image-deliveries/"+d.ID+"/archive", nil, map[string]string{"X-Host-ID": "other", "Authorization": "Bearer " + token}), 403)
	response := call(r, nil, "POST", "/agent/v1/image-deliveries/"+d.ID+"/attempts", map[string]string{"task_id": command.ID}, headers)
	check(t, response, 200)
	var started struct {
		Attempt uint64 `json:"attempt"`
	}
	json.Unmarshal(response.Body.Bytes(), &started)
	if started.Attempt != 2 {
		t.Fatal("attempt history overwritten")
	}
	progress := control.Progress{TaskID: command.ID, Attempt: started.Attempt, Sequence: 1, Phase: "downloading", Bytes: 100, Total: 200, Reused: 50}
	check(t, call(r, nil, "POST", "/agent/v1/image-deliveries/"+d.ID+"/progress", progress, headers), 200)
	check(t, call(r, nil, "POST", "/agent/v1/image-deliveries/"+d.ID+"/progress", progress, headers), 409)
	progress.Attempt = 1
	progress.Sequence = 2
	check(t, call(r, nil, "POST", "/agent/v1/image-deliveries/"+d.ID+"/progress", progress, headers), 409)
	check(t, call(r, nil, "POST", "/agent/v1/results", control.Result{ID: command.ID, Status: "succeeded", Code: "IMAGE_PREPARED", Image: reference, ImageID: imageID, Platform: "linux/arm64"}, headers), 200)
	db.Model(&ImageDelivery{}).Where("id = ?", d.ID).UpdateColumns(map[string]any{"updated_at": time.Now().Add(-8 * 24 * time.Hour)})
	resources, err := s.cleanupResources(db)
	if err != nil {
		t.Fatal(err)
	}
	var imageResource, central CleanupResource
	for _, resource := range resources {
		if resource.Kind == "image" {
			imageResource = resource
		}
		if resource.Kind == "central" {
			central = resource
		}
	}
	if imageResource.ID == "" || imageResource.Reason != "" || central.ID == "" {
		t.Fatal("eligible cleanup resource missing", imageResource.Reason)
	}
	check(t, call(r, owner, "POST", "/api/v1/image-cleanup", map[string]string{"request_id": "cleanup-local", "resource_id": imageResource.ID}, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/image-cleanup/cleanup-local/approve", nil, nil), 409)
	check(t, call(r, reviewer, "POST", "/api/v1/image-cleanup/cleanup-local/approve", nil, nil), 200)
	observations[0].ImageID = imageID
	heartbeat()
	commands = call(r, nil, "GET", "/agent/v1/commands", nil, headers)
	check(t, commands, 200)
	json.Unmarshal(commands.Body.Bytes(), &envs)
	if len(envs) != 0 {
		t.Fatal("current image deletion dispatched")
	}
	var cleanup ImageCleanup
	db.First(&cleanup, "id = ?", "cleanup-local")
	if cleanup.Status != "failed" {
		t.Fatal("changed cleanup not rejected")
	}
	observations[0].ImageID = "sha256:" + strings.Repeat("f", 64)
	heartbeat()
	check(t, call(r, owner, "POST", "/api/v1/image-cleanup", map[string]string{"request_id": "cleanup-central", "resource_id": central.ID}, nil), 200)
	check(t, call(r, reviewer, "POST", "/api/v1/image-cleanup/cleanup-central/approve", nil, nil), 200)
	s.centralCleanupStep()
	cleanup = ImageCleanup{}
	db.First(&cleanup, "id = ?", "cleanup-central")
	if cleanup.Status != "succeeded" {
		t.Fatal("central cleanup failed", cleanup.Code)
	}
	if _, err := os.Stat(filepath.Join(directory, command.Archive.SHA256)); !os.IsNotExist(err) {
		t.Fatal("central archive not removed")
	}
	check(t, call(r, owner, "GET", "/api/v1/image-deliveries/"+d.ID+"/attempts", nil, nil), 200)
}
