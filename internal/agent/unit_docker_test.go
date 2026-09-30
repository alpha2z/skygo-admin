package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerRecoveryUnit(t *testing.T) {
	path := os.Getenv("SKYGO_ADMIN_DOCKER_FIXTURE")
	if path == "" {
		t.Skip("run scripts/docker-smoke.py")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	var f struct {
		API    LocalService `json:"unit_api"`
		Web    LocalService `json:"unit_web"`
		Images []string     `json:"test_images"`
	}
	if json.Unmarshal(b, &f) != nil || f.API.ID == "" || f.Web.ID == "" {
		t.Fatal("unit fixture unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d := Docker{}
	dir := t.TempDir()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	settings.Atomic(filepath.Join(dir, "token"), []byte(strings.Repeat("t", 40)))
	settings.Atomic(filepath.Join(dir, "public"), []byte(base64.StdEncoding.EncodeToString(pub)))
	envFile := filepath.Join(dir, "managed.env")
	originalEnv := "# synthetic managed images\n" + f.API.ImageVariable + "=" + f.Images[0] + "\n" + f.Web.ImageVariable + "=" + f.Images[0] + "\nUNRELATED=preserved\n"
	if os.WriteFile(envFile, []byte(originalEnv), 0600) != nil {
		t.Fatal("env fixture")
	}
	f.API.EnvFile = envFile
	f.Web.EnvFile = envFile
	f.API.SyncImageEnv = true
	f.Web.SyncImageEnv = true
	cfg := Config{HostID: "host", APIURL: "http://127.0.0.1:1", AllowLoopbackHTTP: true, TokenFile: filepath.Join(dir, "token"), PublicKeyFile: filepath.Join(dir, "public"), StateDir: filepath.Join(dir, "state"), Services: []LocalService{f.API, f.Web}, ControlUnit: &LocalUnit{ID: "admin", API: f.API.ID, Web: f.Web.ID}}
	a, err := New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	makeCommand := func(id string, selectedBoth bool, image string) control.Command {
		c := control.Command{Version: 1, ID: id, HostID: "host", Service: f.API.ID, Action: "control-unit", ExpiresAt: time.Now().Add(3 * time.Minute), Unit: &control.UnitPlan{ID: "admin", BootID: a.boot}}
		for i, s := range cfg.Services {
			o, err := d.Observe(ctx, s)
			if err != nil || !o.Healthy {
				t.Fatal("unit member unhealthy")
			}
			rev, err := a.unitRevision(s)
			if err != nil {
				t.Fatal(err)
			}
			target := control.UnitTarget{SyncImageEnv: s.SyncImageEnv, Service: s.ID, Kind: []string{"admin-api", "admin-web"}[i], Platform: o.Platform, PreviousImageID: o.ImageID, Image: o.ImageID, ImageID: o.ImageID, Revision: rev}
			if i == 0 || selectedBoth {
				r := d.Execute(ctx, s, control.Command{Action: "prepare-image", Image: image, Platform: o.Platform})
				if r.Status != "succeeded" {
					t.Fatal("prepare failed", r.Code)
				}
				target.Selected = true
				target.Image = image
				target.ImageID = r.ImageID
			}
			c.Unit.Targets = append(c.Unit.Targets, target)
		}
		return c
	}
	started := func(s LocalService) string {
		ids, err := run(ctx, os.Environ(), append(compose(s), "ps", "-a", "-q", s.ComposeService)...)
		if err != nil {
			t.Fatal("container unavailable")
		}
		raw, err := run(ctx, os.Environ(), "inspect", "--format", "{{.State.StartedAt}}", strings.TrimSpace(string(ids)))
		if err != nil {
			t.Fatal("container timestamp unavailable")
		}
		return string(raw)
	}
	before := started(f.Web)
	c := makeCommand("api-only", false, f.Images[1])
	e, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	r := a.Handle(ctx, e)
	if r.Status != "succeeded" {
		t.Fatal("API-only publication", r.Code)
	}
	rawEnv, _ := os.ReadFile(envFile)
	if !strings.Contains(string(rawEnv), f.API.ImageVariable+"="+f.Images[1]) || !strings.Contains(string(rawEnv), f.Web.ImageVariable+"="+f.Images[0]) || !strings.Contains(string(rawEnv), "UNRELATED=preserved") {
		t.Fatal("selected image env not persisted correctly")
	}
	if started(f.Web) == before {
		t.Fatal("unselected Web did not restart")
	}
	o, _ := d.Observe(ctx, f.Web)
	if o.ImageID != c.Unit.Targets[1].PreviousImageID {
		t.Fatal("unselected image changed")
	}
	beforeAPI, beforeWeb := started(f.API), started(f.Web)
	fresh, err := New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	fresh.recoverUnits(ctx)
	if fresh.Handle(ctx, e).Status != "succeeded" || started(f.API) != beforeAPI || started(f.Web) != beforeWeb {
		t.Fatal("restart duplicated terminal execution")
	}
	// Bring the companion to revision 2, then publish both back to revision 1.
	if d.Execute(ctx, f.Web, control.Command{Action: "deploy", Image: f.Images[1]}).Status != "succeeded" {
		t.Fatal("pair fixture")
	}
	c = makeCommand("pair-update", true, f.Images[0])
	e, err = control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Handle(ctx, e); r.Status != "succeeded" {
		t.Fatal("pair publication", r.Code)
	}
	// A real HTTP health probe fails only for revision 2; both old containers must recover.
	probeService := f.API
	probeService.HealthURL = ""
	nextImage, err := inspectImage(ctx, f.Images[1])
	if err != nil {
		t.Fatal("image fixture")
	}
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed, err := d.Observe(r.Context(), probeService)
		if err != nil || observed.ImageID == nextImage.ID {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer probe.Close()
	originalURL := cfg.Services[0].HealthURL
	cfg.Services[0].HealthURL = probe.URL
	a, err = New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	c = makeCommand("health-rollback", false, f.Images[1])
	e, err = control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Handle(ctx, e); r.Status != "rolled_back" {
		t.Fatal("health failure did not restore pair", r.Code)
	}
	cfg.Services[0].HealthURL = originalURL
	a, err = New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	rawEnv, _ = os.ReadFile(envFile)
	if string(rawEnv) != originalEnv {
		t.Fatal("failed publication did not restore env")
	}
	// Persist an interrupted transition and recover without any controller available.
	c = makeCommand("offline-recovery", true, f.Images[1])
	e, _ = control.Sign(c, key)
	if a.saveUnit(filepath.Join(cfg.StateDir, c.ID+".unit.json"), unitJournal{Envelope: e, Phase: "starting"}) != nil {
		t.Fatal("journal fixture")
	}
	if a.planEnv(ctx, c.ID, cfg.Services, []string{f.Images[1], f.Images[1]}) != nil || a.applyEnv(c.ID, false) != nil {
		t.Fatal("interrupted env write fixture")
	}
	if d.UnitStop(ctx, f.Web) != nil || d.UnitStop(ctx, f.API) != nil || d.UnitStart(ctx, f.API, f.Images[1]) != nil {
		t.Fatal("interruption fixture")
	}
	fresh, err = New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	fresh.recoverUnits(ctx)
	if fresh.Handle(ctx, e).Status != "rolled_back" {
		t.Fatal("offline recovery failed")
	}
	rawEnv, _ = os.ReadFile(envFile)
	if string(rawEnv) != originalEnv {
		t.Fatal("offline recovery did not restore env")
	}
	beforeAPI, beforeWeb = started(f.API), started(f.Web)
	again, err := New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	again.recoverUnits(ctx)
	if started(f.API) != beforeAPI || started(f.Web) != beforeWeb {
		t.Fatal("recovery replay changed containers")
	}
	// Ordinary approved deployments use the same durable environment ownership.
	a, err = New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	service := a.cfg.Services[0]
	observed, _ := d.Observe(ctx, service)
	revision, _ := a.unitRevision(service)
	normal := control.Command{Version: 1, ID: "env-single", HostID: cfg.HostID, Service: service.ID, Action: "deploy", Image: f.Images[1], SyncImageEnv: true, InventoryRevision: revision, PreviousImageID: observed.ImageID, ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := control.Sign(normal, key)
	if err != nil {
		t.Fatal(err)
	}
	if result := a.Handle(ctx, signed); result.Status != "succeeded" {
		t.Fatal("ordinary env deployment failed", result.Code)
	}
	persisted, _ := os.ReadFile(envFile)
	if !strings.Contains(string(persisted), service.ImageVariable+"="+f.Images[1]) {
		t.Fatal("ordinary env not persisted")
	}
	stamp := started(service)
	a, err = New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	a.recoverEnvDeployments(ctx)
	if started(service) != stamp {
		t.Fatal("ordinary completed deployment repeated")
	}
	observed, _ = d.Observe(ctx, service)
	revision, _ = a.unitRevision(service)
	normal.ID = "env-single-rollback"
	normal.Image = f.Images[0]
	normal.InventoryRevision = revision
	normal.PreviousImageID = observed.ImageID
	signed, err = control.Sign(normal, key)
	if err != nil {
		t.Fatal(err)
	}
	a.driver = failedDeployment{Docker: d}
	if result := a.Handle(ctx, signed); result.Code != "DEPLOYMENT_ROLLED_BACK" {
		t.Fatal("ordinary failure not restored", result.Code)
	}
	restored, _ := os.ReadFile(envFile)
	if string(restored) != string(persisted) {
		t.Fatal("ordinary rollback lost old env")
	}

}

type failedDeployment struct{ Docker }

func (d failedDeployment) Execute(ctx context.Context, s LocalService, c control.Command) control.Result {
	r := d.Docker.Execute(ctx, s, c)
	if c.Action == "deploy" && r.Status == "succeeded" {
		r.Status = "uncertain"
		r.Code = "SYNTHETIC_LOST_VERIFICATION"
	}
	return r
}
