package agent

import (
	"context"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryPullDisabledDoesNotRunPull(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	s := LocalService{ID: "worker", RegistryPullDisabled: false, AllowedImages: []string{"ghcr.io/example/worker"}}
	r := prepareImage(context.Background(), s, control.Command{ID: "test", Action: "prepare-image", Image: "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64"})
	if r.Code != "CENTRAL_DISTRIBUTION_REQUIRED" {
		t.Fatalf("unexpected result: %+v", r)
	}
	raw, _ := os.ReadFile(log)
	if strings.Contains(string(raw), "pull") {
		t.Fatal("registry pull executed")
	}
}

func TestDeploymentNeverPullsMissingImage(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, action := range []string{"deploy", "rollback"} {
		s := LocalService{ID: "worker", AllowedImages: []string{"ghcr.io/example/worker"}}
		r := (Docker{}).Execute(context.Background(), s, control.Command{ID: "test", Action: action, Image: "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)})
		if r.Code != "CENTRAL_DISTRIBUTION_REQUIRED" {
			t.Fatalf("%s: %+v", action, r)
		}
	}
	raw, _ := os.ReadFile(log)
	if strings.Contains(string(raw), "pull") || strings.Contains(string(raw), " up ") {
		t.Fatal("missing image caused mutation")
	}
}
