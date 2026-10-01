package agent

import (
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowScopeSeparatesManagedImagesFromPrivateConfiguration(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "env")
	compose := filepath.Join(dir, "compose.json")
	if err := os.WriteFile(compose, []byte(`{"services":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{token: "synthetic-local-token", cfg: Config{Services: []LocalService{{ID: "one", EnvFile: env, ComposeFile: compose, ImageVariable: "ONE"}, {ID: "two", EnvFile: env, ComposeFile: compose, ImageVariable: "TWO"}}}}
	write := func(first, second, other string) {
		t.Helper()
		if err := os.WriteFile(env, []byte("# retain comments\nONE=example/one@sha256:"+strings.Repeat(first, 64)+"\nTWO=example/two@sha256:"+strings.Repeat(second, 64)+"\nOTHER="+other+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	observe := func() control.Observation {
		var o control.Observation
		a.decorateWorkflow(a.cfg.Services[0], &o)
		return o
	}
	write("a", "b", "synthetic")
	old := observe()
	if len(old.ScopeRevision) != 64 {
		t.Fatal("scope unavailable")
	}
	write("c", "d", "synthetic")
	next := observe()
	if next.ScopeRevision != old.ScopeRevision || next.ConfiguredImage == old.ConfiguredImage {
		t.Fatal("authorized image changes not isolated")
	}
	write("c", "d", "changed")
	if observe().ScopeRevision == old.ScopeRevision {
		t.Fatal("private config drift ignored")
	}
	f, _ := os.OpenFile(env, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("ONE=duplicate\n")
	f.Close()
	if observe().ScopeRevision != "" {
		t.Fatal("ambiguous env accepted")
	}
}
