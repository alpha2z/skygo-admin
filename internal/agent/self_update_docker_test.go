package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
)

// Run inside an isolated host-network Linux test container. The old/new images
// and executable are synthetic, local-only fixtures; no Registry credentials.
func TestAgentSelfUpdateRealDocker(t *testing.T) {
	dir := os.Getenv("SKYGO_ADMIN_UPDATE_FIXTURE")
	if dir == "" {
		t.Skip("run scripts/agent-update-smoke.py")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	old := os.Getenv("SKYGO_ADMIN_UPDATE_OLD")
	next := os.Getenv("SKYGO_ADMIN_UPDATE_NEW")
	if !control.ImageIdentity(old) || !control.ImageIdentity(next) {
		t.Fatal("fixture images missing")
	}
	platform := os.Getenv("SKYGO_ADMIN_UPDATE_PLATFORM")
	project := "agent-update-" + control.Digest([]byte(dir))[:12]
	oldRef := "ghcr.io/example/synthetic@sha256:" + strings.Repeat("a", 64)
	newRef := "ghcr.io/example/synthetic@sha256:" + strings.Repeat("b", 64)
	s := LocalService{ID: "agent", ComposeFile: filepath.Join(dir, "compose.json"), EnvFile: filepath.Join(dir, "compose.env"), Project: project, ComposeService: "agent", ImageVariable: "AGENT_IMAGE", SyncImageEnv: true, AllowedImages: []string{"ghcr.io/example/synthetic"}, CacheDir: filepath.Join(dir, "state", "images")}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	settings.Atomic(filepath.Join(dir, "token"), []byte(strings.Repeat("t", 40)))
	settings.Atomic(filepath.Join(dir, "public"), []byte(base64.StdEncoding.EncodeToString(pub)))
	settings.Atomic(s.EnvFile, []byte("AGENT_IMAGE="+oldRef+"\n"))
	os.MkdirAll(s.CacheDir, 0700)
	for ref, id := range map[string]string{oldRef: old, newRef: next} {
		raw, _ := json.Marshal(imageEvidence{Reference: ref, Platform: platform, RuntimeID: id})
		settings.Atomic(evidencePath(s, ref), raw)
	}
	var mu sync.Mutex
	var heartbeat control.Heartbeat
	var envelope *control.Envelope
	var result control.Result
	var originalBoot string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-Host-ID") != "host" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 40) {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/agent/v1/heartbeat":
			json.NewDecoder(r.Body).Decode(&heartbeat)
			w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/agent/v1/commands":
			if envelope == nil || result.Status == "succeeded" || result.Status == "rolled_back" {
				w.Write([]byte(`[]`))
			} else {
				json.NewEncoder(w).Encode([]control.Envelope{*envelope})
			}
		case r.URL.Path == "/agent/v1/results":
			var incoming control.Result
			json.NewDecoder(r.Body).Decode(&incoming)
			if result.Status != "succeeded" && result.Status != "rolled_back" {
				result = incoming
			}
			w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/confirmation"):
			if heartbeat.BootID == originalBoot {
				w.WriteHeader(409)
				return
			}
			for _, o := range heartbeat.Observations {
				if o.Service == s.ID && o.Running && o.Healthy {
					json.NewEncoder(w).Encode(map[string]string{"image_id": o.ImageID})
					return
				}
			}
			w.WriteHeader(409)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	cfg := Config{HostID: "host", APIURL: api.URL, AllowLoopbackHTTP: true, TokenFile: filepath.Join(dir, "token"), PublicKeyFile: filepath.Join(dir, "public"), StateDir: filepath.Join(dir, "state"), SelfService: s.ID, UpdaterSocket: "/run/agent-updater-smoke/updater.sock", Services: []LocalService{s}}
	raw, _ := json.Marshal(cfg)
	settings.Atomic(filepath.Join(dir, "agent.json"), raw)
	composeDoc := map[string]any{"services": map[string]any{"agent": map[string]any{"image": "${AGENT_IMAGE}", "network_mode": "host", "entrypoint": []string{filepath.Join(dir, "ops-agent")}, "command": []string{"-config", filepath.Join(dir, "agent.json")}, "volumes": []string{dir + ":" + dir, "/var/run/docker.sock:/var/run/docker.sock", "runtime:/run/agent-updater-smoke"}}}, "volumes": map[string]any{"runtime": map[string]any{"external": true, "name": os.Getenv("SKYGO_ADMIN_UPDATE_SOCKET_VOLUME")}}}
	raw, _ = json.Marshal(composeDoc)
	settings.Atomic(s.ComposeFile, raw)
	defer run(context.Background(), os.Environ(), append(compose(s), "down", "--timeout", "10")...)
	updater, err := NewUpdater(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = updater.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		updater.Stop(stop)
	}()
	if err = (Docker{}).UnitStart(ctx, s, old); err != nil {
		t.Fatal(err)
	}
	wait := func(check func() bool) {
		t.Helper()
		for {
			if check() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("Docker update timed out")
			case <-time.After(time.Second):
			}
		}
	}
	wait(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(heartbeat.Observations) > 0 && heartbeat.Observations[0].ImageID == old
	})
	a, err := New(cfg, Docker{})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := a.unitRevision(a.cfg.Services[0])
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	originalBoot = heartbeat.BootID
	mu.Unlock()
	c := control.Command{Version: 1, ID: "real-update", HostID: "host", Service: s.ID, Action: "upgrade-agent", Image: newRef, Platform: platform, PreviousImageID: old, InventoryRevision: revision, ExpiresAt: time.Now().Add(time.Minute), AgentUpdate: &control.AgentUpdate{ImageID: next, BootID: originalBoot, ReleaseID: "ci-1-1", PreparationID: "verified-fixture"}}
	signed, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	envelope = &signed
	mu.Unlock()
	wait(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return result.Status == "succeeded" || result.Status == "rolled_back"
	})
	mu.Lock()
	final := result
	newBoot := heartbeat.BootID
	mu.Unlock()
	if final.Status != "succeeded" || newBoot == originalBoot {
		t.Fatalf("update failed: %+v", final)
	}
	o, err := (Docker{}).Observe(ctx, s)
	if err != nil || o.ImageID != next || !updater.a.envMatches(c.ID, true) {
		t.Fatal("container or environment mismatch")
	}
}
