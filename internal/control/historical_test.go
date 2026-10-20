package control

import (
	"strings"
	"testing"
	"time"
)

func TestHistoricalCommandsRestrictedToScopedRollback(t *testing.T) {
	c := Command{Version: 1, ID: "rollback", Service: "service", HostID: "node", Action: "rollback", Image: "example/old:v1", HistoricalBaselineID: strings.Repeat("a", 64), WorkflowID: "workflow", SyncImageEnv: true, InventoryRevision: strings.Repeat("b", 64), PreviousImageID: "sha256:" + strings.Repeat("c", 64), ExpiresAt: time.Now().Add(time.Hour)}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"deploy", "prepare-image", "no-workflow", "no-env", "bad-id", "legacy-agent"} {
		t.Run(kind, func(t *testing.T) {
			x := c
			switch kind {
			case "deploy", "prepare-image":
				x.Action = kind
			case "no-workflow":
				x.WorkflowID = ""
			case "no-env":
				x.SyncImageEnv = false
			case "bad-id":
				x.HistoricalBaselineID = "../invalid"
			case "legacy-agent":
				x.HistoricalBaselineID = ""
			}
			if x.Validate() == nil {
				t.Fatal("unsafe command accepted")
			}
		})
	}
}
