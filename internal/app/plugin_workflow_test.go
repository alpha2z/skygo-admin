package app

import (
	"context"
	"encoding/json"
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
