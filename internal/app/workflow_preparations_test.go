package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/plugin"
)

func TestWorkflowPreparationExpiryDoesNotPreventStartedRecoveryMySQL(t *testing.T) {
	db, _ := releaseDB(t)
	_, plan, o := pluginWorkflowFixture()
	now := time.Now().UTC().Truncate(time.Millisecond)
	cmd := control.Command{Image: o.ConfiguredImage, PluginRevision: o.PluginRevision}
	payload, _ := json.Marshal(cmd)
	result, _ := json.Marshal(control.Result{ID: "prepared-one", Status: "succeeded", ImageID: o.ImageID, Platform: "linux/amd64"})
	task := Task{ID: "prepared-one", ServiceID: "worker", HostID: "host", Action: "prepare-image", Status: "succeeded", Payload: string(payload), Result: string(result), FinishedAt: &now, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	plan.Preparations = []plugin.PreparedImage{{TaskID: task.ID, Service: "worker", Host: "host", Image: cmd.Image, ImageID: o.ImageID, Platform: "linux/amd64", PluginRevision: cmd.PluginRevision, FinishedAt: now}}
	w := Workflow{ID: "reviewed-workflow"}
	if err := checkWorkflowPreparations(db, w, plan, true); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-10 * time.Minute)
	plan.Preparations[0].FinishedAt = old
	db.Model(&task).Update("finished_at", old)
	if checkWorkflowPreparations(db, w, plan, true) == nil {
		t.Fatal("expired approval accepted")
	}
	if checkWorkflowPreparations(db, w, plan, false) == nil {
		t.Fatal("expired first dispatch accepted")
	}
	if err := db.Create(&Task{ID: "already-sent", WorkflowID: w.ID, Status: "dispatched", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := checkWorkflowPreparations(db, w, plan, false); err != nil {
		t.Fatal("age prevented recovery", err)
	}
	db.Model(&task).Update("result", `{}`)
	if checkWorkflowPreparations(db, w, plan, false) == nil {
		t.Fatal("receipt tampering accepted during recovery")
	}
}
