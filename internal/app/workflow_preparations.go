package app

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"gorm.io/gorm"
)

// A receipt must still be fresh when approval or the first dispatch occurs.
// Once execution may have started, age cannot prevent a frozen rollback plan.
func checkWorkflowPreparations(tx *gorm.DB, w Workflow, p WorkflowPlan, initial bool) error {
	if len(p.Preparations) == 0 {
		return nil
	}
	if len(p.Preparations) > 64 {
		return errors.New("too many workflow preparation requirements")
	}
	started := int64(0)
	if !initial && w.ID != "" {
		if tx.Model(&Task{}).Where("workflow_id = ? AND status NOT IN ?", w.ID, []string{"queued", "pending"}).Count(&started).Error != nil {
			return errors.New("workflow execution history unavailable")
		}
	}
	seen := map[string]bool{}
	for _, required := range p.Preparations {
		if seen[required.Service] || !control.Identifier.MatchString(required.TaskID) {
			return errors.New("invalid preparation requirement")
		}
		seen[required.Service] = true
		inScope := false
		for _, target := range p.Targets {
			inScope = inScope || target.Service == required.Service && target.Host == required.Host && target.PluginRevision == required.PluginRevision
		}
		if !inScope {
			return errors.New("preparation requirement outside workflow scope")
		}
		var task Task
		var command control.Command
		var result control.Result
		if tx.First(&task, "id = ?", required.TaskID).Error != nil || task.Status != "succeeded" || task.Action != "prepare-image" || task.ServiceID != required.Service || task.HostID != required.Host || task.FinishedAt == nil || json.Unmarshal([]byte(task.Payload), &command) != nil || json.Unmarshal([]byte(task.Result), &result) != nil {
			return errors.New("verified preparation receipt unavailable")
		}
		if command.Image != required.Image || command.PluginRevision != required.PluginRevision || result.ID != task.ID || result.Status != "succeeded" || result.ImageID != required.ImageID || result.Platform != required.Platform || !task.FinishedAt.Equal(required.FinishedAt) {
			return errors.New("preparation receipt identity changed")
		}
		if started == 0 && (time.Since(*task.FinishedAt) > preparationTTL || task.FinishedAt.After(time.Now().Add(time.Second))) {
			return errors.New("image preparation receipt expired; prepare again")
		}
	}
	return nil
}
