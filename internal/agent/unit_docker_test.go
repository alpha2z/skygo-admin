package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/baseline"
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

	testHistoricalRollback(t, ctx, a, cfg, key, service, f.Images[0])
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

func testHistoricalRollback(t *testing.T, ctx context.Context, a *Agent, cfg Config, key ed25519.PrivateKey, service LocalService, next string) {
	t.Helper()
	a.driver = Docker{}
	old, err := a.driver.Observe(ctx, service)
	if err != nil {
		t.Fatal(err)
	}
	tag := "skygo-admin/historical-fixture:" + control.Digest([]byte(t.TempDir()))[:12]
	if _, err = run(ctx, os.Environ(), "image", "tag", old.ImageID, tag); err != nil {
		t.Fatal(err)
	}
	defer run(context.Background(), os.Environ(), "image", "rm", tag)
	raw, _ := os.ReadFile(service.EnvFile)
	raw = replaceLine(raw, service.ImageVariable, service.ImageVariable+"="+tag, true)
	if err = os.WriteFile(service.EnvFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, scope, err := a.workflowConfiguration(service)
	if err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.tar")
	_, err = run(ctx, os.Environ(), "image", "save", "--output", exportPath, tag)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.StateDir, "baselines")
	os.MkdirAll(dir, 0700)
	b := baseline.Record{Version: 1, Host: cfg.HostID, Service: service.ID, Tag: tag, ImageID: old.ImageID, Platform: old.Platform, ScopeRevision: scope, ArchiveSHA256: control.Digest(archive), ArchiveSize: int64(len(archive)), Operator: "integration-test", Reason: "historical rollback fixture", CreatedAt: time.Now().UTC()}
	path := filepath.Join(dir, b.ArchiveSHA256+".tar")
	os.WriteFile(path, archive, 0600)
	b.ConfigID, err = baseline.ArchiveConfigID(path)
	if err != nil {
		t.Fatal(err)
	}
	id, envelope, err := baseline.Sign(b, key)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, archive, 0600)
	os.WriteFile(filepath.Join(dir, id+".json"), envelope, 0600)
	os.WriteFile(filepath.Join(dir, service.ID+".active"), []byte(id), 0600)
	a.decorateWorkflow(service, &old)
	if old.HistoricalBaselineID != id {
		_, _, e := a.activeBaseline(service)
		t.Fatal("historical identity not reported", e)
	}
	command := func(name, action, image, baselineID string) control.Command {
		o, err := a.driver.Observe(ctx, service)
		if err != nil {
			t.Fatal(err)
		}
		a.decorateWorkflow(service, &o)
		rev, _ := a.unitRevision(service)
		return control.Command{Version: 1, ID: name, HostID: cfg.HostID, Service: service.ID, Action: action, Image: image, HistoricalBaselineID: baselineID, WorkflowID: "historical-test", WorkflowImage: o.ConfiguredImage, WorkflowScopeRevision: o.ScopeRevision, WorkflowBaselineID: o.HistoricalBaselineID, SyncImageEnv: true, InventoryRevision: rev, PreviousImageID: o.ImageID, ExpiresAt: time.Now().Add(time.Minute)}
	}
	a.driver = failedDeployment{Docker: Docker{}}
	c := command("baseline-failed-deploy", "deploy", next, "")
	signed, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Handle(ctx, signed); r.Code != "DEPLOYMENT_ROLLED_BACK" {
		t.Fatal("historical automatic recovery failed", r)
	}
	a.driver = Docker{}
	c = command("baseline-deploy", "deploy", next, "")
	signed, _ = control.Sign(c, key)
	if r := a.Handle(ctx, signed); r.Status != "succeeded" {
		t.Fatal("historical upgrade failed", r)
	}
	c = command("baseline-corrupt", "rollback", tag, id)
	signed, _ = control.Sign(c, key)
	os.WriteFile(path, []byte("damaged"), 0600)
	if r := a.Handle(ctx, signed); r.Status == "succeeded" {
		t.Fatal("corrupt archive accepted")
	}
	os.WriteFile(path, archive, 0600)
	c = command("baseline-restore", "rollback", tag, id)
	signed, err = control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Handle(ctx, signed); r.Status != "succeeded" {
		t.Fatal("historical rollback failed", r)
	}
	fresh, err := New(cfg, Docker{})
	if err != nil {
		t.Fatal(err)
	}
	fresh.recoverEnvDeployments(ctx)
	if r := fresh.Handle(ctx, signed); r.Status != "succeeded" {
		t.Fatal("historical receipt not replayed", r)
	}
	restored, _ := fresh.driver.Observe(ctx, service)
	fresh.decorateWorkflow(service, &restored)
	if restored.ImageID != b.ImageID || restored.HistoricalBaselineID != id {
		t.Fatal("historical baseline not restored")
	}
	if _, err = run(ctx, os.Environ(), "image", "tag", next, tag); err != nil {
		t.Fatal(err)
	}
	moved := restored
	fresh.decorateWorkflow(service, &moved)
	// Fresh observations must not carry a stale baseline after a tag moves.
	var empty control.Observation
	empty.ImageID = restored.ImageID
	empty.Platform = restored.Platform
	fresh.decorateWorkflow(service, &empty)
	if empty.HistoricalBaselineID != "" {
		t.Fatal("moved tag accepted")
	}
	run(ctx, os.Environ(), "image", "tag", b.ImageID, tag)
}
