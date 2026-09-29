package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"testing"
	"time"
)

func TestDockerServiceLifecycle(t *testing.T) {
	path := os.Getenv("SKYGO_ADMIN_DOCKER_FIXTURE")
	if path == "" {
		t.Skip("run scripts/docker-smoke.py")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	var s LocalService
	if json.Unmarshal(raw, &s) != nil {
		t.Fatal("invalid fixture")
	}
	d := Docker{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	o, err := d.Observe(ctx, s)
	if err != nil || !o.Healthy {
		t.Fatal("example node not healthy")
	}
	for _, action := range []string{"stop", "start", "restart", "health"} {
		r := d.Execute(ctx, s, control.Command{ID: "smoke-" + action, Action: action})
		if r.Status != "succeeded" {
			t.Fatalf("%s failed: %s", action, r.Code)
		}
	}
	var fixture struct {
		Images []string `json:"test_images"`
	}
	json.Unmarshal(raw, &fixture)
	if len(fixture.Images) != 2 {
		t.Fatal("two image versions required")
	}
	before, err := run(ctx, os.Environ(), append(compose(s), "ps", "-a", "-q", s.ComposeService)...)
	if err != nil {
		t.Fatal("container fixture unavailable")
	}
	beforeConfig, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		t.Fatal("config fixture unavailable")
	}
	observed, err := d.Observe(ctx, s)
	if err != nil || observed.Platform == "" {
		t.Fatal("platform unavailable")
	}
	for _, image := range fixture.Images {
		for attempt := 0; attempt < 2; attempt++ {
			r := d.Execute(ctx, s, control.Command{ID: "smoke-prepare", Action: "prepare-image", Image: image, Platform: observed.Platform})
			if r.Status != "succeeded" || r.ImageID == "" || r.Platform != observed.Platform {
				t.Fatalf("image preparation failed: %s", r.Code)
			}
		}
	}
	after, _ := run(ctx, os.Environ(), append(compose(s), "ps", "-a", "-q", s.ComposeService)...)
	afterConfig, _ := os.ReadFile(s.ConfigPath)
	if !bytes.Equal(before, after) || !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("image preparation changed container or configuration")
	}
	for i, image := range []string{fixture.Images[0], fixture.Images[1], fixture.Images[0]} {
		action := "deploy"
		if i == 2 {
			action = "rollback"
		}
		r := d.Execute(ctx, s, control.Command{ID: "smoke-image", Action: action, Image: image})
		if r.Status != "succeeded" || r.Image != image {
			t.Fatalf("image transition %d failed: %s", i, r.Code)
		}
	}
	content, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		t.Fatal("registry unavailable")
	}
	r := d.Execute(ctx, s, control.Command{ID: "smoke-config", Action: "configure", Config: content, ConfigHash: control.Digest(content)})
	if r.Status != "succeeded" || r.ConfigHash != control.Digest(content) {
		t.Fatal("configuration activation failed")
	}
	r = d.Execute(ctx, s, control.Command{ID: "smoke-logs", Action: "logs"})
	if r.Code != "LOG_ACCESS_DISABLED" {
		t.Fatal("logs policy bypassed")
	}
}
