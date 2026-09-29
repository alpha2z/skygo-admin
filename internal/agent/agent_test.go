package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDriver struct {
	mu          sync.Mutex
	calls       int
	observation control.Observation
}

func (d *fakeDriver) Observe(context.Context, LocalService) (control.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.observation, nil
}
func (d *fakeDriver) Execute(_ context.Context, _ LocalService, c control.Command) control.Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.observation = control.Observation{Service: c.Service, Image: c.Image, Healthy: true}
	return control.Result{ID: c.ID, Status: "succeeded", Code: "VERIFIED", Image: c.Image, Healthy: true}
}
func fixture(t *testing.T) (*Agent, ed25519.PrivateKey, *fakeDriver) {
	t.Helper()
	dir := t.TempDir()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	settings.Atomic(filepath.Join(dir, "token"), []byte(strings.Repeat("t", 40)))
	os.WriteFile(filepath.Join(dir, "public"), []byte(base64.StdEncoding.EncodeToString(pub)), 0600)
	d := &fakeDriver{}
	a, err := New(Config{HostID: "host-a", APIURL: "http://127.0.0.1:1234", AllowLoopbackHTTP: true, TokenFile: filepath.Join(dir, "token"), PublicKeyFile: filepath.Join(dir, "public"), StateDir: filepath.Join(dir, "state"), Services: []LocalService{{ID: "worker", ComposeFile: "/tmp/example.yaml", Project: "example", ComposeService: "worker", ImageVariable: "WORKER_IMAGE"}}}, d)
	if err != nil {
		t.Fatal(err)
	}
	return a, key, d
}
func command() control.Command {
	return control.Command{Version: 1, ID: "command-a", HostID: "host-a", Service: "worker", Action: "deploy", Image: "example/worker@sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
}
func TestConcurrentDuplicatesAndRestart(t *testing.T) {
	a, key, d := fixture(t)
	e, _ := control.Sign(command(), key)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.Handle(context.Background(), e).Status != "succeeded" {
				t.Error("execution failed")
			}
		}()
	}
	wg.Wait()
	if d.calls != 1 {
		t.Fatal("duplicate execution")
	}
	fresh, err := New(a.cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Handle(context.Background(), e).Status != "succeeded" || d.calls != 1 {
		t.Fatal("restart repeated execution")
	}
}
func TestExpiredAndTamperedCommandsNeverExecute(t *testing.T) {
	a, key, d := fixture(t)
	c := command()
	c.ExpiresAt = time.Now().Add(-time.Minute)
	e, _ := control.Sign(c, key)
	if a.Handle(context.Background(), e).Code != "COMMAND_EXPIRED" {
		t.Fatal("expiry ignored")
	}
	e.Payload = append(e.Payload, ' ')
	a.Handle(context.Background(), e)
	if d.calls != 0 {
		t.Fatal("rejected command executed")
	}
}
func TestInterruptedIntentReconcilesWithoutExecution(t *testing.T) {
	a, key, d := fixture(t)
	c := command()
	e, _ := control.Sign(c, key)
	rec := receipt{Digest: control.Digest(e.Payload), Command: c, Result: control.Result{ID: c.ID, Status: "uncertain"}}
	b, _ := json.Marshal(rec)
	settings.Atomic(filepath.Join(a.cfg.StateDir, c.ID+".json"), b)
	r := a.Handle(context.Background(), e)
	if r.Status != "uncertain" || d.calls != 0 {
		t.Fatal("uncertain operation retried")
	}
	d.observation = control.Observation{Image: c.Image, Healthy: true}
	r = a.Handle(context.Background(), e)
	if r.Status != "succeeded" || r.Code != "RECONCILED" || d.calls != 0 {
		t.Fatal("reconciliation failed")
	}
}
func TestLogRedaction(t *testing.T) {
	got := redact("ready\nAuthorization: Bearer private-value\npassword=private-value")
	if strings.Contains(got, "private-value") || !strings.Contains(got, "ready") {
		t.Fatal("sensitive lines retained")
	}
}

func TestExplicitOperatorResolutionDoesNotReexecute(t *testing.T) {
	a, key, d := fixture(t)
	c := command()
	e, _ := control.Sign(c, key)
	rec := receipt{Digest: control.Digest(e.Payload), Command: c, Result: control.Result{ID: c.ID, Status: "uncertain"}}
	if a.save(filepath.Join(a.cfg.StateDir, c.ID+".json"), rec) != nil {
		t.Fatal("fixture")
	}
	if a.ResolveFailed(c.ID) != nil {
		t.Fatal("resolution failed")
	}
	r := a.Handle(context.Background(), e)
	if r.Status != "failed" || r.Code != "OPERATOR_RESOLVED_FAILED" || d.calls != 0 {
		t.Fatal("resolution repeated operation")
	}
	if a.ResolveFailed(c.ID) == nil {
		t.Fatal("terminal receipt changed")
	}
}
