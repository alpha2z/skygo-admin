package control

import (
	"strings"
	"testing"
	"time"
)

func TestUnitScopeRejectsExpansionAndCompanionReplacement(t *testing.T) {
	old := "sha256:" + strings.Repeat("a", 64)
	next := "sha256:" + strings.Repeat("b", 64)
	base := Command{Version: 1, ID: "task", HostID: "host", Service: "api", Action: "control-unit", ExpiresAt: time.Now().Add(time.Minute)}
	targets := []UnitTarget{{Service: "api", Kind: "admin-api", Platform: "linux/arm64", PreviousImageID: old, Image: "example/api@sha256:" + strings.Repeat("c", 64), ImageID: next, Revision: strings.Repeat("d", 64), Selected: true}, {Service: "web", Kind: "admin-web", Platform: "linux/arm64", PreviousImageID: old, Image: old, ImageID: old, Revision: strings.Repeat("d", 64)}}
	for _, name := range []string{"companion", "third", "duplicate", "ordinary", "empty"} {
		t.Run(name, func(t *testing.T) {
			c := base
			c.Unit = &UnitPlan{ID: "admin", BootID: "boot", Targets: append([]UnitTarget(nil), targets...)}
			if c.Validate() != nil {
				t.Fatal("fixture invalid")
			}
			switch name {
			case "companion":
				c.Unit.Targets[1].Image = next
			case "third":
				c.Unit.Targets = append(c.Unit.Targets, c.Unit.Targets[1])
			case "duplicate":
				c.Unit.Targets[1].Service = "api"
			case "ordinary":
				c.Action = "restart"
			case "empty":
				c.Unit.Targets[0].Selected = false
				c.Unit.Targets[0].Image = old
				c.Unit.Targets[0].ImageID = old
			}
			if c.Validate() == nil {
				t.Fatal("unauthorized scope accepted")
			}
		})
	}
}
