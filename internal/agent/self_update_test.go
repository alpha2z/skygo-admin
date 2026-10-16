package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
)

func updaterFixture(t *testing.T) (*Updater, control.Command, ed25519.PrivateKey, *unitFake) {
	t.Helper()
	a, key, _ := fixture(t)
	dir := t.TempDir()
	old := "sha256:" + strings.Repeat("a", 64)
	next := "sha256:" + strings.Repeat("b", 64)
	ref := "ghcr.io/example/ops-agent@sha256:" + strings.Repeat("c", 64)
	s := LocalService{ID: "agent", ComposeFile: filepath.Join(dir, "compose.json"), EnvFile: filepath.Join(dir, "compose.env"), Project: "fixture", ComposeService: "agent", ImageVariable: "AGENT_IMAGE", SyncImageEnv: true, AllowedImages: []string{"ghcr.io/example/ops-agent"}, CacheDir: filepath.Join(dir, "images")}
	os.MkdirAll(s.CacheDir, 0700)
	os.WriteFile(s.ComposeFile, []byte(`{"services":{"agent":{"image":"${AGENT_IMAGE}"}}}`), 0600)
	os.WriteFile(s.EnvFile, []byte("AGENT_IMAGE=ghcr.io/example/ops-agent@sha256:"+strings.Repeat("a", 64)+"\n"), 0600)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!" + python + "\nimport sys,json,os\na=sys.argv[1:]\nif a[:2]==['image','inspect']:\n print(json.dumps({'Id':'" + old + "' if a[-1]=='" + old + "' else '" + next + "','Os':'linux','Architecture':'amd64','RepoDigests':['" + ref + "']}))\nelif 'config' in a:\n print(json.dumps({'services':{'agent':{'image':'${AGENT_IMAGE}' if '--no-interpolate' in a else os.environ.get('AGENT_IMAGE','old')}}}))\nelse: sys.exit(1)\n"
	os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	d := &unitFake{state: map[string]control.Observation{"agent": {Service: "agent", ImageID: old, Running: true, Healthy: true}}, images: map[string]string{ref: next}}
	a.driver = d
	a.cfg.ControlUnit = nil
	a.cfg.SelfService = "agent"
	a.cfg.Services = []LocalService{s}
	a.cfg.StateDir = filepath.Join(dir, "updater")
	os.MkdirAll(a.cfg.StateDir, 0700)
	u := &Updater{a: a, service: s, directory: a.cfg.StateDir}
	revision, err := a.unitRevision(s)
	if err != nil {
		t.Fatal(err)
	}
	c := control.Command{Version: 1, ID: "update-1", HostID: a.cfg.HostID, Service: s.ID, Action: "upgrade-agent", Image: ref, Platform: "linux/amd64", PreviousImageID: old, InventoryRevision: revision, AgentUpdate: &control.AgentUpdate{PreparationID: "prepare-1", ImageID: next, BootID: "old-boot", ReleaseID: "ci-1-1"}, ExpiresAt: time.Now().Add(time.Minute)}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/confirmation") {
			o, _ := d.Observe(r.Context(), s)
			json.NewEncoder(w).Encode(map[string]string{"image_id": o.ImageID})
		} else {
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(api.Close)
	a.cfg.APIURL = api.URL
	return u, c, key, d
}
func submitUpdate(t *testing.T, u *Updater, c control.Command, key ed25519.PrivateKey) *httptest.ResponseRecorder {
	t.Helper()
	e, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	w := httptest.NewRecorder()
	u.accept(w, httptest.NewRequest("POST", "/update", bytes.NewReader(raw)))
	return w
}
func TestAgentUpdaterHandoffSuccessAndReplay(t *testing.T) {
	u, c, key, d := updaterFixture(t)
	if w := submitUpdate(t, u, c, key); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(d.calls) != 0 {
		t.Fatal("handoff mutated container")
	}
	u.tick(context.Background())
	j, err := u.load(u.path(c.ID))
	if err != nil || j.Result.Status != "succeeded" || !j.Delivered {
		t.Fatalf("journal: %+v %v", j, err)
	}
	if !u.a.envMatches(c.ID, true) {
		t.Fatal("image variable not persisted")
	}
	before := len(d.calls)
	if w := submitUpdate(t, u, c, key); w.Code != 200 {
		t.Fatal(w.Code)
	}
	u.tick(context.Background())
	if len(d.calls) != before {
		t.Fatal("replay repeated mutation")
	}
}
func TestAgentUpdaterFailedStartRestoresImageAndEnvironment(t *testing.T) {
	u, c, key, d := updaterFixture(t)
	d.fail = c.AgentUpdate.ImageID
	if w := submitUpdate(t, u, c, key); w.Code != 200 {
		t.Fatal(w.Code)
	}
	u.tick(context.Background())
	j, _ := u.load(u.path(c.ID))
	if j.Result.Status != "rolled_back" || !u.a.envMatches(c.ID, false) {
		t.Fatalf("rollback failed: %+v", j)
	}
	o, _ := d.Observe(context.Background(), u.service)
	if o.ImageID != c.PreviousImageID {
		t.Fatal("old image not restored")
	}
}
func TestAgentUpdaterInterruptedWriteOnlyRollsBack(t *testing.T) {
	u, c, key, d := updaterFixture(t)
	if w := submitUpdate(t, u, c, key); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := u.a.planEnv(context.Background(), c.ID, []LocalService{u.service}, []string{c.Image}); err != nil {
		t.Fatal(err)
	}
	j, _ := u.load(u.path(c.ID))
	j.Phase = "writing"
	u.save(j)
	u.tick(context.Background())
	j, _ = u.load(u.path(c.ID))
	if j.Result.Status != "rolled_back" || len(d.calls) != 2 {
		t.Fatalf("unexpected recovery: %+v %v", j, d.calls)
	}
}
func TestAgentUpdaterRejectsExpiryScopeDriftAndTampering(t *testing.T) {
	u, c, key, _ := updaterFixture(t)
	expired := c
	expired.ExpiresAt = time.Now().Add(-time.Second)
	if submitUpdate(t, u, expired, key).Code == 200 {
		t.Fatal("expired accepted")
	}
	changed := c
	changed.Service = "other"
	if submitUpdate(t, u, changed, key).Code == 200 {
		t.Fatal("foreign service accepted")
	}
	changed = c
	changed.InventoryRevision = strings.Repeat("f", 64)
	if submitUpdate(t, u, changed, key).Code == 200 {
		t.Fatal("drift accepted")
	}
	e, _ := control.Sign(c, key)
	e.Signature[0] ^= 1
	raw, _ := json.Marshal(e)
	w := httptest.NewRecorder()
	u.accept(w, httptest.NewRequest("POST", "/update", bytes.NewReader(raw)))
	if w.Code != 403 {
		t.Fatal("tampered command accepted")
	}
	if submitUpdate(t, u, c, key).Code != 200 {
		t.Fatal("valid rejected")
	}
	second := c
	second.ID = "update-2"
	if submitUpdate(t, u, second, key).Code != 409 {
		t.Fatal("concurrent upgrade accepted")
	}
}
func TestAgentUpdaterWaitsForControllerConfirmation(t *testing.T) {
	u, c, key, d := updaterFixture(t)
	if submitUpdate(t, u, c, key).Code != 200 {
		t.Fatal("handoff")
	}
	if err := u.a.planEnv(context.Background(), c.ID, []LocalService{u.service}, []string{c.Image}); err != nil {
		t.Fatal(err)
	}
	if err := u.a.applyEnv(c.ID, false); err != nil {
		t.Fatal(err)
	}
	d.UnitStart(context.Background(), u.service, c.AgentUpdate.ImageID)
	j, _ := u.load(u.path(c.ID))
	j.Phase = "confirming"
	u.save(j)
	u.a.cfg.APIURL = "http://127.0.0.1:1"
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	u.tick(ctx)
	j, _ = u.load(u.path(c.ID))
	if j.Result.Status == "succeeded" || j.Phase == "terminal" {
		t.Fatal("unconfirmed update finalized")
	}
	if len(d.calls) > 2 {
		t.Fatal("shutdown triggered rollback mutation")
	}
}
