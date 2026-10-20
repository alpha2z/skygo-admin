package app

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/baseline"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/plugin"
)

func pluginWorkflowFixture() (PluginProviderConfig, WorkflowPlan, control.Observation) {
	revision := strings.Repeat("a", 64)
	image := "example/service@sha256:" + strings.Repeat("b", 64)
	id := "sha256:" + strings.Repeat("c", 64)
	def, _ := json.Marshal(control.Service{ID: "worker", HostID: "host", PluginID: "example", Image: image})
	p := PluginProviderConfig{Endpoint: plugin.Endpoint{ID: "example", Socket: "/run/example.sock", Revision: revision}, Services: []string{"worker"}, Actions: []PluginActionConfig{{Name: "maintenance"}}}
	o := control.Observation{Service: "worker", ScopeRevision: strings.Repeat("d", 64), PluginRevision: revision, ImageID: id, ConfiguredImage: image, Running: true, Healthy: true}
	plan := WorkflowPlan{Group: "example", Kind: "stop", Reason: "Operator maintenance", Targets: []WorkflowTarget{{Service: "worker", Host: "host", Definition: string(def), ScopeRevision: o.ScopeRevision, PluginRevision: revision, Image: image, ImageID: id}}, Steps: []WorkflowStep{{Label: "Stop", Command: control.Command{HostID: "host", Service: "worker", Action: "stop", PluginRevision: revision}}}}
	return p, plan, o
}

func TestPluginWorkflowAuthorization(t *testing.T) {
	for _, name := range []string{"valid", "scope", "revision", "action", "extension", "definition-host", "control-plane", "rollback"} {
		t.Run(name, func(t *testing.T) {
			p, plan, _ := pluginWorkflowFixture()
			allowed := map[string]bool{"stop": true, "extension": true}
			switch name {
			case "scope":
				p.Services = []string{"other"}
			case "revision":
				plan.Targets[0].PluginRevision = strings.Repeat("e", 64)
			case "action":
				allowed = map[string]bool{"health": true}
			case "extension":
				plan.Steps[0].Command.Action = "extension"
				plan.Steps[0].Command.Extension = "unapproved"
				plan.Steps[0].Command.Payload = json.RawMessage(`{}`)
			case "definition-host":
				var d control.Service
				json.Unmarshal([]byte(plan.Targets[0].Definition), &d)
				d.HostID = "other"
				b, _ := json.Marshal(d)
				plan.Targets[0].Definition = string(b)
			case "control-plane":
				var d control.Service
				json.Unmarshal([]byte(plan.Targets[0].Definition), &d)
				d.ControlPlane = true
				b, _ := json.Marshal(d)
				plan.Targets[0].Definition = string(b)
			case "rollback":
				plan.Rollback = append(plan.Rollback, plan.Steps[0])
				plan.Rollback[0].Command.PluginRevision = ""
			}
			err := validatePluginWorkflow(p, allowed, plan)
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}

func TestWorkflowPluginDriftAndObservationModeMySQL(t *testing.T) {
	db, _ := releaseDB(t)
	_, plan, o := pluginWorkflowFixture()
	if err := db.Create(&ServiceRecord{ID: "worker", Definition: plan.Targets[0].Definition}).Error; err != nil {
		t.Fatal(err)
	}
	h := Host{ID: "host", Active: true, LastSeen: time.Now()}
	write := func() {
		b, _ := json.Marshal([]control.Observation{o})
		h.Observations = string(b)
		if err := db.Save(&h).Error; err != nil {
			t.Fatal(err)
		}
	}
	write()
	s := &Server{}
	if err := s.checkWorkflowScope(db, Workflow{}, plan, true); err != nil {
		t.Fatal(err)
	}
	o.PluginRevision = strings.Repeat("f", 64)
	write()
	if s.checkWorkflowScope(db, Workflow{}, plan, true) == nil {
		t.Fatal("plugin drift accepted")
	}
	o.PluginRevision = plan.Targets[0].PluginRevision
	o.ObservationOnly = true
	write()
	if s.checkWorkflowScope(db, Workflow{}, plan, true) == nil {
		t.Fatal("observation-only mutation accepted")
	}
	plan.CacheOnly = true
	plan.Steps[0].Command.Action = "prepare-image"
	if s.checkWorkflowScope(db, Workflow{}, plan, true) == nil {
		t.Fatal("observation-only cache mutation accepted")
	}
	plan.CacheOnly = false
	plan.Steps[0].Command.Action = "health"
	if err := s.checkWorkflowScope(db, Workflow{}, plan, true); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeWorkflowSnapshotAndPlannerScopeMySQL(t *testing.T) {
	db, _ := releaseDB(t)
	p, plan, observation := pluginWorkflowFixture()
	directory, err := os.MkdirTemp("", "wf-plugin-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	p.Socket = filepath.Join(directory, "p.sock")
	p.Workflows = []PluginWorkflowConfig{{Name: "example-flow", Permission: "ops.write", ApprovalPermission: "ops.approve", Services: []string{"worker"}, Actions: []string{"stop"}}}
	if err = db.Create(&ServiceRecord{ID: "worker", Definition: plan.Targets[0].Definition}).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]control.Observation{observation})
	if err = db.Create(&Host{ID: "host", Active: true, LastSeen: time.Now(), Observations: string(raw), TokenHash: "never-forward-this-secret-hash"}).Error; err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: plugin.Handler(p.Endpoint, func(ctx context.Context, r plugin.Request) (any, error) {
		if strings.Contains(string(r.Payload), "never-forward") {
			t.Error("host credentials exposed")
		}
		if r.Operation == "workflow-plan" {
			var input plugin.WorkflowPlanningRequest
			if json.Unmarshal(r.Payload, &input) != nil || len(input.Inventory) != 1 || input.RequestID != "request-one" {
				t.Error("invalid bounded planner context")
			}
			return plan, nil
		}
		if r.Operation == "workflow-validate" {
			return nil, nil
		}
		t.Errorf("unexpected operation %s", r.Operation)
		return nil, nil
	})}
	go server.Serve(listener)
	defer server.Close()
	cfg := Config{}
	if err = installPluginWorkflows(&cfg, p); err != nil {
		t.Fatal(err)
	}
	provider := cfg.Workflows["example-flow"]
	got, err := provider.Resolve(context.Background(), db, "request-one", json.RawMessage(`{"reason":"fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	if planHash(got) != planHash(plan) {
		t.Fatal("plan changed after validation")
	}
	observation.ObservationOnly = true
	raw, _ = json.Marshal([]control.Observation{observation})
	db.Model(&Host{}).Where("id = ?", "host").Update("observations", string(raw))
	if _, err = provider.Resolve(context.Background(), db, "request-one", json.RawMessage(`{}`)); err == nil {
		t.Fatal("readonly scope accepted before preview")
	}
	p.Workflows[0].Services = []string{"not-approved"}
	if installPluginWorkflows(&Config{}, p) == nil {
		t.Fatal("expanded local configuration accepted")
	}
}

func TestPluginPreparationAllowsOnlyReadinessSubset(t *testing.T) {
	p, plan, _ := pluginWorkflowFixture()
	p.Services = append(p.Services, "unselected")
	plan.CacheOnly = true
	plan.Kind = "prepare"
	cmd := &plan.Steps[0].Command
	cmd.Action = "prepare-image"
	cmd.Image = plan.Targets[0].Image
	cmd.Platform = "linux/amd64"
	plan.Steps[0].ImageID = plan.Targets[0].ImageID
	allowed := map[string]bool{"prepare-image": true, "stop": true}
	if err := validatePluginWorkflow(p, allowed, plan); err != nil {
		t.Fatal(err)
	}
	plan.CacheOnly = false
	if validatePluginWorkflow(p, allowed, plan) == nil {
		t.Fatal("partial lifecycle scope accepted")
	}
	plan.CacheOnly = true
	plan.Steps[0].Command.Action = "stop"
	if validatePluginWorkflow(p, allowed, plan) == nil {
		t.Fatal("mutation disguised as preparation")
	}
	plan.Steps[0].Command = *cmd
	plan.Steps[0].Command.Action = "prepare-image"
	plan.Rollback = append(plan.Rollback, plan.Steps[0])
	if validatePluginWorkflow(p, allowed, plan) == nil {
		t.Fatal("cache-only rollback accepted")
	}
}

func TestRuntimePlannerUsesHistoricalTrustMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	p, plan, o := pluginWorkflowFixture()
	cfg.HistoricalBaselinesDirectory = t.TempDir()
	config := []byte(`{"os":"linux","architecture":"amd64"}`)
	imageID := "sha256:" + control.Digest(config)
	tag := "example/worker:historical"
	manifest, _ := json.Marshal([]map[string]any{{"Config": "config.json", "RepoTags": []string{tag}, "Layers": []string{}}})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, entry := range []struct {
		name string
		raw  []byte
	}{{"config.json", config}, {"manifest.json", manifest}} {
		tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.raw))})
		tw.Write(entry.raw)
	}
	tw.Close()
	r := baseline.Record{Version: 1, Host: "host", Service: "worker", Tag: tag, ImageID: imageID, ConfigID: imageID, Platform: "linux/amd64", ScopeRevision: o.ScopeRevision, ArchiveSHA256: control.Digest(archive.Bytes()), ArchiveSize: int64(archive.Len()), Operator: "fixture", Reason: "historical workflow regression", CreatedAt: time.Now().UTC()}
	id, envelope, err := baseline.Sign(r, cfg.SigningKey)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cfg.HistoricalBaselinesDirectory, r.ArchiveSHA256+".tar"), archive.Bytes(), 0600)
	recordPath := filepath.Join(cfg.HistoricalBaselinesDirectory, id+".json")
	os.WriteFile(recordPath, envelope, 0600)
	plan.Targets[0].HistoricalBaselineID = id
	plan.Targets[0].Image = tag
	plan.Targets[0].ImageID = imageID
	o.HistoricalBaselineID = id
	o.ConfiguredImage = tag
	o.ImageID = imageID
	o.Capabilities = []string{"workflow.historical-baseline.v1"}
	if err = db.Create(&ServiceRecord{ID: "worker", Definition: plan.Targets[0].Definition}).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]control.Observation{o})
	if err = db.Create(&Host{ID: "host", Active: true, LastSeen: time.Now(), Observations: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "wf-baseline-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p.Socket = filepath.Join(dir, "p.sock")
	p.Workflows = []PluginWorkflowConfig{{Name: "example-flow", Permission: "ops.write", ApprovalPermission: "ops.approve", Services: []string{"worker"}, Actions: []string{"stop"}}}
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: plugin.Handler(p.Endpoint, func(ctx context.Context, request plugin.Request) (any, error) {
		if request.Operation == "workflow-plan" {
			return plan, nil
		}
		return nil, nil
	})}
	go server.Serve(listener)
	defer server.Close()
	if err = installPluginWorkflows(&cfg, p); err != nil {
		t.Fatal(err)
	}
	resolve := func() error {
		_, e := cfg.Workflows["example-flow"].Resolve(context.Background(), db, "request-one", json.RawMessage(`{}`))
		return e
	}
	if err = resolve(); err != nil {
		t.Fatal("registered baseline failed through runtime plugin", err)
	}
	os.Remove(recordPath)
	if resolve() == nil {
		t.Fatal("revoked historical trust accepted")
	}
	if (&Server{cfg: Config{SigningKey: ed25519.PrivateKey{}}}).checkHistoricalBaselines(plan) == nil {
		t.Fatal("missing signing identity accepted")
	}
}
