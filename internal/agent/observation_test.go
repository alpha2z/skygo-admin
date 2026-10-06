package agent

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"strings"
	"testing"
)

func TestObservationOnlyRejectsEveryNonHealthCommand(t *testing.T) {
	a, key, d := fixture(t)
	a.cfg.Services[0].ObservationOnly = true
	for _, action := range []string{"start", "stop", "restart", "deploy", "rollback", "configure", "logs", "prepare-image", "image-cleanup", "extension"} {
		c := command()
		c.ID = "observe-" + action
		c.Action = action
		switch action {
		case "start", "stop", "restart", "logs":
			c.Image = ""
		case "configure":
			c.Image = ""
			c.Config = json.RawMessage(`{}`)
			c.ConfigHash = control.Digest(c.Config)
		case "extension":
			c.Image = ""
			c.Extension = "sample"
			c.Payload = json.RawMessage(`{}`)
		case "prepare-image":
			c.Platform = "linux/amd64"
		case "image-cleanup":
			c.Cleanup = &control.Cleanup{Kind: "image", ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
		}
		e, err := control.Sign(c, key)
		if err != nil {
			t.Fatal(action, err)
		}
		if r := a.Handle(context.Background(), e); r.Code != "OBSERVATION_ONLY" || d.calls != 0 {
			t.Fatalf("%s escaped observation mode: %+v", action, r)
		}
	}
	c := command()
	c.ID = "observe-health"
	c.Action = "health"
	c.Image = ""
	e, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Handle(context.Background(), e); r.Status != "succeeded" {
		t.Fatal(r)
	}
}
