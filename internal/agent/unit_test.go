package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type unitFake struct {
	mu     sync.Mutex
	state  map[string]control.Observation
	images map[string]string
	calls  []string
	fail   string
}

func (d *unitFake) Observe(_ context.Context, s LocalService) (control.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state[s.ID], nil
}
func (d *unitFake) Execute(context.Context, LocalService, control.Command) control.Result {
	panic("ordinary driver must not execute unit")
}
func (d *unitFake) UnitImage(_ context.Context, _ LocalService, ref, id, _ string) error {
	if ref == id || d.images[ref] == id {
		return nil
	}
	return errors.New("missing")
}
func (d *unitFake) UnitStop(_ context.Context, s LocalService) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "stop:"+s.ID)
	o := d.state[s.ID]
	o.Healthy = false
	o.Running = false
	d.state[s.ID] = o
	return nil
}
func (d *unitFake) UnitStart(_ context.Context, s LocalService, image string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "start:"+s.ID)
	if image == d.fail {
		return errors.New("synthetic startup failure")
	}
	id := image
	if d.images[image] != "" {
		id = d.images[image]
	}
	d.state[s.ID] = control.Observation{Service: s.ID, ImageID: id, Healthy: true, Running: true}
	return nil
}
func unitFixture(t *testing.T) (*Agent, ed25519.PrivateKey, *unitFake, control.Command) {
	t.Helper()
	a, key, _ := fixture(t)
	path := filepath.Join(t.TempDir(), "compose.json")
	os.WriteFile(path, []byte(`{"services":{}}`), 0600)
	a.cfg.Services = []LocalService{{ID: "api", ComposeFile: path, Project: "example", ComposeService: "api", ImageVariable: "API_IMAGE", HealthURL: "http://127.0.0.1/healthz"}, {ID: "web", ComposeFile: path, Project: "example", ComposeService: "web", ImageVariable: "WEB_IMAGE", HealthURL: "http://127.0.0.1/healthz"}}
	a.cfg.ControlUnit = &LocalUnit{ID: "admin", API: "api", Web: "web"}
	old := "sha256:" + strings.Repeat("a", 64)
	next := "sha256:" + strings.Repeat("b", 64)
	ref := "example/api@sha256:" + strings.Repeat("c", 64)
	d := &unitFake{state: map[string]control.Observation{"api": {ImageID: old, Healthy: true, Running: true}, "web": {ImageID: old, Healthy: true, Running: true}}, images: map[string]string{ref: next}}
	a.driver = d
	c := control.Command{Version: 1, ID: "unit-test", HostID: a.cfg.HostID, Service: "api", Action: "control-unit", ExpiresAt: time.Now().Add(time.Minute), Unit: &control.UnitPlan{ID: "admin", BootID: a.boot}}
	for i, s := range a.cfg.Services {
		rev, err := a.unitRevision(s)
		if err != nil {
			t.Fatal(err)
		}
		target := control.UnitTarget{Service: s.ID, Kind: []string{"admin-api", "admin-web"}[i], Platform: "linux/arm64", PreviousImageID: old, Image: old, ImageID: old, Revision: rev}
		if i == 0 {
			target.Selected = true
			target.Image = ref
			target.ImageID = next
		}
		c.Unit.Targets = append(c.Unit.Targets, target)
	}
	return a, key, d, c
}
func TestUnitCompanionRestartAndConcurrentReplay(t *testing.T) {
	a, key, d, c := unitFixture(t)
	e, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := a.Handle(context.Background(), e); r.Status != "succeeded" {
				t.Error(r.Code)
			}
		}()
	}
	wg.Wait()
	want := []string{"stop:web", "stop:api", "start:api", "start:web"}
	if !reflect.DeepEqual(d.calls, want) {
		t.Fatalf("unexpected mutations: %v", d.calls)
	}
	fresh, err := New(a.cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	fresh.recoverUnits(context.Background())
	if r := fresh.Handle(context.Background(), e); r.Status != "succeeded" || !reflect.DeepEqual(d.calls, want) {
		t.Fatal("restart repeated upgrade")
	}
	if d.state["web"].ImageID != c.Unit.Targets[1].PreviousImageID {
		t.Fatal("companion image changed")
	}
}
func TestUnitFailureRestoresBothAndRetainsReceipt(t *testing.T) {
	a, key, d, c := unitFixture(t)
	d.fail = c.Unit.Targets[0].Image
	e, _ := control.Sign(c, key)
	r := a.Handle(context.Background(), e)
	if r.Status != "rolled_back" || len(r.Unit) != 2 {
		t.Fatalf("rollback not verified: %s", r.Code)
	}
	for _, o := range d.state {
		if !o.Healthy || o.ImageID != c.Unit.Targets[0].PreviousImageID {
			t.Fatal("old image not restored")
		}
	}
	calls := len(d.calls)
	if a.Handle(context.Background(), e).Status != "rolled_back" || len(d.calls) != calls {
		t.Fatal("rollback replay mutated")
	}
}
func TestUnitCrashRecoveryOfflineAndScopeDrift(t *testing.T) {
	for _, phase := range []string{"stopping", "starting", "verifying", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			a, key, d, c := unitFixture(t)
			e, _ := control.Sign(c, key)
			path := filepath.Join(a.cfg.StateDir, c.ID+".unit.json")
			if a.saveUnit(path, unitJournal{Envelope: e, Phase: phase}) != nil {
				t.Fatal("journal")
			}
			o := d.state["api"]
			o.Healthy = false
			o.Running = false
			d.state["api"] = o
			fresh, err := New(a.cfg, d)
			if err != nil {
				t.Fatal(err)
			}
			fresh.recoverUnits(context.Background())
			if r := fresh.Handle(context.Background(), e); r.Status != "rolled_back" {
				t.Fatalf("offline recovery failed: %s", r.Code)
			}
			if d.state["api"].ImageID != c.Unit.Targets[0].PreviousImageID {
				t.Fatal("forward upgrade repeated")
			}
		})
	}
	a, key, d, c := unitFixture(t)
	e, _ := control.Sign(c, key)
	a.saveUnit(filepath.Join(a.cfg.StateDir, c.ID+".unit.json"), unitJournal{Envelope: e, Phase: "stopping"})
	os.WriteFile(a.cfg.Services[0].ComposeFile, []byte("changed"), 0600)
	if a.Handle(context.Background(), e).Status != "uncertain" || len(d.calls) != 0 {
		t.Fatal("configuration drift executed")
	}
}
func TestUnitRejectsExpiredWrongBootAndMissingImages(t *testing.T) {
	for _, test := range []string{"expired", "boot", "image", "revision"} {
		t.Run(test, func(t *testing.T) {
			a, key, d, c := unitFixture(t)
			switch test {
			case "expired":
				c.ExpiresAt = time.Now().Add(-time.Minute)
			case "boot":
				c.Unit.BootID = "other"
			case "image":
				d.images = map[string]string{}
			case "revision":
				c.Unit.Targets[0].Revision = strings.Repeat("f", 64)
			}
			e, _ := control.Sign(c, key)
			r := a.Handle(context.Background(), e)
			if r.Code != "UNIT_PREFLIGHT_REJECTED" || len(d.calls) != 0 {
				t.Fatal("unsafe preflight")
			}
		})
	}
}
