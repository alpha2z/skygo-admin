package app

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) centralPolicy(tx *gorm.DB, c control.Command) error {
	switch c.Action {
	case "prepare-image", "deploy", "rollback", "control-unit", "upgrade-agent":
		if s.distribution == nil {
			return publicationError("CENTRAL_DISTRIBUTION_REQUIRED", "Configure Admin image distribution before installing images.")
		}
	}
	if c.Action == "prepare-image" && c.Archive == nil {
		return publicationError("CENTRAL_DISTRIBUTION_REQUIRED", "Recreate legacy image preparation through Admin distribution.")
	}
	// A frozen workflow recovery may restore its original, locally verified image
	// even when that installation predates central delivery history.
	if c.Action == "rollback" && c.WorkflowID != "" {
		var w Workflow
		var plan WorkflowPlan
		if tx.First(&w, "id = ?", c.WorkflowID).Error == nil && w.RollingBack && json.Unmarshal([]byte(w.Plan), &plan) == nil {
			for _, target := range plan.Targets {
				if target.Host == c.HostID && target.Service == c.Service && target.Image == c.Image && control.ImageIdentity(target.ImageID) {
					return nil
				}
			}
		}
	}
	if c.Action == "deploy" || c.Action == "rollback" {
		var count int64
		if tx.Model(&Task{}).Joins("JOIN image_deliveries ON image_deliveries.task_id = tasks.id").Where("tasks.host_id = ? AND tasks.service_id = ? AND tasks.status = ? AND image_deliveries.image = ? AND image_deliveries.mode = ?", c.HostID, c.Service, "succeeded", c.Image, "archive").Count(&count).Error != nil || count == 0 {
			return publicationError("PREPARATION_REQUIRED", "Prepare this image through Admin before installation.")
		}
	}
	return nil
}
func updateHostBlocked(tx *gorm.DB, host, own string) bool {
	var count int64
	return tx.Model(&Task{}).Where("host_id = ? AND id <> ? AND action = ? AND status IN ?", host, own, "upgrade-agent", []string{"queued", "dispatched", "uncertain"}).Count(&count).Error != nil || count > 0
}
func (s *Server) agentUpdateTarget(tx *gorm.DB, service, releaseID string) (control.Service, Host, control.Observation, string, error) {
	var record ServiceRecord
	var def control.Service
	var h Host
	var seen []control.Observation
	fail := func() (control.Service, Host, control.Observation, string, error) {
		return def, h, control.Observation{}, "", publicationError("AGENT_UPDATER_REQUIRED", "Install a transitional Agent and its independent updater, then refresh inventory.")
	}
	if s.distribution == nil {
		return def, h, control.Observation{}, "", publicationError("CENTRAL_DISTRIBUTION_REQUIRED", "Configure Admin image distribution first.")
	}
	if tx.First(&record, "id = ?", service).Error != nil || json.Unmarshal([]byte(record.Definition), &def) != nil || def.ControlKind != "ops-agent" || !def.ControlPlane {
		return fail()
	}
	if tx.First(&h, "id = ?", def.HostID).Error != nil || !h.Active || time.Since(h.LastSeen) > time.Minute || json.Unmarshal([]byte(h.Observations), &seen) != nil {
		return fail()
	}
	var observation control.Observation
	for _, o := range seen {
		if o.Service == service {
			observation = o
		}
	}
	if !hasCapability(observation, "agent.self-update.v1") || !hasCapability(observation, "image.archive.v1") || !observation.SyncImageEnv || !observation.Running || !observation.Healthy || !control.ImageIdentity(observation.ImageID) || len(observation.InventoryRevision) != 64 {
		return fail()
	}
	m, err := s.trustedRelease(tx, releaseID)
	if err != nil {
		return def, h, observation, "", err
	}
	for _, image := range m.Images {
		if image.Service == "ops-agent" && image.Platform == def.Platform && image.Platform == observation.Platform {
			return def, h, observation, image.Reference, nil
		}
	}
	return fail()
}
func (s *Server) prepareAgentUpdate(c *gin.Context) { s.agentUpdateMutation(c, true) }
func (s *Server) createAgentUpdate(c *gin.Context)  { s.agentUpdateMutation(c, false) }
func (s *Server) agentUpdateMutation(c *gin.Context, prepare bool) {
	var req struct {
		RequestID     string `json:"request_id"`
		Service       string `json:"service"`
		ReleaseID     string `json:"release_id"`
		PreparationID string `json:"preparation_id"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if !control.Identifier.MatchString(req.RequestID) {
		c.Status(400)
		return
	}
	action := "upgrade-agent"
	if prepare {
		action = "prepare-image"
	}
	s.change(c, body, "agent-update."+action, req.Service, func(tx *gorm.DB) (any, error) {
		var old Task
		if err := tx.First(&old, "id = ?", req.RequestID).Error; err == nil {
			var cmd control.Command
			if json.Unmarshal([]byte(old.Payload), &cmd) != nil || old.RequestedBy != uint32(c.GetUint("admin_id")) || old.ServiceID != req.Service || old.Action != action {
				return nil, errConflict
			}
			if prepare {
				var d ImageDelivery
				if tx.First(&d, "id = ?", old.DeliveryID).Error != nil || d.ReleaseID != req.ReleaseID {
					return nil, errConflict
				}
			} else if cmd.AgentUpdate == nil || (cmd.AgentUpdate.ReleaseID != req.ReleaseID || cmd.AgentUpdate.PreparationID != req.PreparationID) {
				return nil, errConflict
			}
			return unchangedMutation{old}, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		def, h, o, image, err := s.agentUpdateTarget(tx, req.Service, req.ReleaseID)
		if err != nil {
			return nil, err
		}
		if updateHostBlocked(tx, h.ID, "") {
			return nil, publicationError("AGENT_UPDATE_BLOCKING", "Resolve the active Agent update first.")
		}
		now := time.Now().UTC()
		cmd := control.Command{Version: 1, PluginRevision: o.PluginRevision, ID: req.RequestID, HostID: h.ID, Service: def.ID, Action: action, Image: image, Platform: def.Platform, ExpiresAt: now.Add(time.Hour)}
		task := Task{SingleConfirmation: !s.cfg.IndependentApprovalEnabled, ID: cmd.ID, HostID: h.ID, ServiceID: def.ID, Action: action, RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: now, ExpiresAt: cmd.ExpiresAt, Status: "pending"}
		if !prepare {
			var p Task
			var result control.Result
			var pc control.Command
			if tx.First(&p, "id = ?", req.PreparationID).Error != nil || p.HostID != h.ID || p.ServiceID != def.ID || p.Action != "prepare-image" || p.Status != "succeeded" || p.FinishedAt == nil || time.Since(*p.FinishedAt) > preparationTTL || json.Unmarshal([]byte(p.Result), &result) != nil || json.Unmarshal([]byte(p.Payload), &pc) != nil || pc.Archive == nil || pc.Image != image || !s.preparedImageIdentity(pc, result.ImageID) {
				return nil, publicationError("PREPARATION_REQUIRED", "Prepare the selected Agent image first.")
			}
			cmd.AgentUpdate = &control.AgentUpdate{PreparationID: req.PreparationID, ImageID: result.ImageID, BootID: h.BootID, ReleaseID: req.ReleaseID}
			cmd.PreviousImageID = o.ImageID
			cmd.InventoryRevision = o.InventoryRevision
		}
		if cmd.Validate() != nil {
			return nil, errConflict
		}
		raw, _ := json.Marshal(cmd)
		task.Payload = string(raw)
		task.PayloadHash = control.Digest(raw)
		if prepare {
			if err := s.queuePreparation(tx, &task, req.ReleaseID, h).Error; err != nil {
				return nil, err
			}
			return task, nil
		}
		if tx.Create(&task).Error != nil {
			return nil, errConflict
		}
		if task.SingleConfirmation {
			return s.authorizeTask(c, tx, task.ID)
		}
		return task, nil
	})
}
func (s *Server) checkAgentUpdate(tx *gorm.DB, c control.Command) error {
	if c.Action != "upgrade-agent" {
		return nil
	}
	if c.AgentUpdate == nil {
		return errConflict
	}
	def, h, o, image, err := s.agentUpdateTarget(tx, c.Service, c.AgentUpdate.ReleaseID)
	if err != nil {
		return err
	}
	if def.HostID != c.HostID || h.BootID != c.AgentUpdate.BootID || o.ImageID != c.PreviousImageID || o.InventoryRevision != c.InventoryRevision || image != c.Image {
		return publicationError("AGENT_UPDATE_SCOPE_CHANGED", "Agent inventory changed; prepare a fresh update.")
	}
	var preparation Task
	var prepared control.Result
	var source control.Command
	if tx.First(&preparation, "id = ?", c.AgentUpdate.PreparationID).Error != nil || preparation.Status != "succeeded" || preparation.HostID != c.HostID || preparation.ServiceID != c.Service || preparation.FinishedAt == nil || time.Since(*preparation.FinishedAt) > preparationTTL || json.Unmarshal([]byte(preparation.Payload), &source) != nil || source.Archive == nil || source.Image != c.Image || json.Unmarshal([]byte(preparation.Result), &prepared) != nil || prepared.ImageID != c.AgentUpdate.ImageID {
		return publicationError("PREPARATION_REQUIRED", "Refresh the centrally prepared Agent image before execution.")
	}
	var count int64
	if tx.Model(&Task{}).Where("host_id = ? AND id <> ? AND status IN ?", h.ID, c.ID, []string{"preparing", "queued", "dispatched", "uncertain"}).Count(&count).Error != nil || count > 0 {
		return publicationError("HOST_BUSY", "Finish or reconcile host tasks before updating the Agent.")
	}
	var records []ServiceRecord
	if tx.Find(&records).Error != nil {
		return errConflict
	}
	for _, record := range records {
		var d control.Service
		if json.Unmarshal([]byte(record.Definition), &d) != nil {
			return errConflict
		}
		if d.HostID == h.ID && workflowLocked(tx, d.ID, "") {
			return publicationError("HOST_BUSY", "Finish host workflows before updating the Agent.")
		}
	}
	return nil
}
func (s *Server) agentUpdateConfirmation(c *gin.Context) {
	var t Task
	var cmd control.Command
	var h Host
	var observations []control.Observation
	if s.db.First(&t, "id = ? AND host_id = ?", c.Param("id"), c.GetString("host_id")).Error != nil || t.Action != "upgrade-agent" || (t.Status != "dispatched" && t.Status != "uncertain") || json.Unmarshal([]byte(t.Payload), &cmd) != nil || cmd.AgentUpdate == nil || s.db.First(&h, "id = ?", t.HostID).Error != nil || h.BootID == cmd.AgentUpdate.BootID || time.Since(h.LastSeen) > 15*time.Second || json.Unmarshal([]byte(h.Observations), &observations) != nil {
		c.Status(409)
		return
	}
	for _, o := range observations {
		if o.Service == cmd.Service && o.Running && o.Healthy && (o.ImageID == cmd.AgentUpdate.ImageID || o.ImageID == cmd.PreviousImageID) {
			c.JSON(200, gin.H{"image_id": o.ImageID})
			return
		}
	}
	c.Status(409)
}
