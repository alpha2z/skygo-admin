package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WorkflowProvider resolves application intents into a bounded, reviewable plan.
// Applications supply business policy; this package owns approval and execution.
type WorkflowProvider struct {
	Permission         string
	ApprovalPermission string
	Resolve            func(context.Context, *gorm.DB, string, json.RawMessage) (WorkflowPlan, error)
	Validate           func(context.Context, *gorm.DB, WorkflowPlan) error
}
type WorkflowTarget struct {
	Service       string `json:"service"`
	Host          string `json:"host"`
	Definition    string `json:"definition"`
	ScopeRevision string `json:"scope_revision"`
	Image         string `json:"image"`
	ImageID       string `json:"image_id"`
}
type WorkflowStep struct {
	ImageIDAlternatives []string        `json:"image_id_alternatives,omitempty"`
	Label               string          `json:"label"`
	Command             control.Command `json:"command"`
	ImageID             string          `json:"image_id,omitempty"`
	When                string          `json:"when,omitempty"` // running or stopped; conditions cannot add scope.
}
type WorkflowPlan struct {
	Group     string           `json:"group"`
	Kind      string           `json:"kind"`
	Title     string           `json:"title"`
	Reason    string           `json:"reason"`
	CacheOnly bool             `json:"cache_only"`
	Targets   []WorkflowTarget `json:"targets"`
	Steps     []WorkflowStep   `json:"steps"`
	Rollback  []WorkflowStep   `json:"rollback,omitempty"`
	Context   json.RawMessage  `json:"context,omitempty"`
}
type Workflow struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	Provider    string    `gorm:"size:64" json:"provider"`
	Group       string    `gorm:"column:scope_group;size:128;index" json:"group"`
	Kind        string    `gorm:"size:32" json:"kind"`
	RequestHash string    `gorm:"size:64" json:"-"`
	Plan        string    `gorm:"type:mediumtext" json:"plan"`
	Status      string    `gorm:"size:32;index" json:"status"`
	Code        string    `gorm:"size:160" json:"code"`
	RequestedBy uint32    `json:"requested_by"`
	ApprovedBy  uint32    `json:"approved_by"`
	Position    int       `json:"position"`
	RollingBack bool      `json:"rolling_back"`
	ChildID     string    `gorm:"size:64" json:"child_id"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type WorkflowLock struct {
	Resource   string `gorm:"primaryKey;size:160"`
	WorkflowID string `gorm:"size:64;index"`
}
type workflowInput struct {
	RequestID   string          `json:"request_id"`
	Provider    string          `json:"provider"`
	Intent      json.RawMessage `json:"intent"`
	PreviewHash string          `json:"preview_hash"`
}

func (s *Server) workflowProvider(c *gin.Context, id string, approve bool) (WorkflowProvider, bool) {
	p, ok := s.cfg.Workflows[id]
	permission := p.Permission
	if approve {
		permission = p.ApprovalPermission
	}
	yes, _ := s.auth.Enforce(c.GetString("admin_role"), permission)
	if !ok || !yes {
		c.Status(403)
		return p, false
	}
	return p, true
}
func planHash(p WorkflowPlan) string { b, _ := json.Marshal(p); return control.Digest(b) }
func validWorkflowPlan(p WorkflowPlan) error {
	if len(p.Targets) == 0 || len(p.Targets) > 64 || len(p.Steps) == 0 || len(p.Steps) > 512 || len(p.Rollback) > 512 || len(p.Group) > 128 || p.Group == "" || len(p.Reason) > 512 || strings.TrimSpace(p.Reason) == "" {
		return errors.New("invalid workflow scope")
	}
	targets := map[string]string{}
	for _, t := range p.Targets {
		if targets[t.Service] != "" || !control.Identifier.MatchString(t.Service) || !control.Identifier.MatchString(t.Host) || len(t.ScopeRevision) != 64 || !control.Image.MatchString(t.Image) || !validRuntimeImageID(t.ImageID) {
			return errors.New("invalid workflow target")
		}
		targets[t.Service] = t.Host
	}
	all := append(append([]WorkflowStep{}, p.Steps...), p.Rollback...)
	for _, step := range all {
		c := step.Command
		if targets[c.Service] != c.HostID || targets[c.Service] == "" || len(step.Label) > 120 || (step.When != "" && step.When != "running" && step.When != "stopped") {
			return errors.New("step outside scope")
		}
		if p.CacheOnly && c.Action != "prepare-image" {
			return errors.New("cache-only workflow cannot mutate services")
		}
		switch c.Action {
		case "prepare-image", "extension", "stop", "start", "deploy", "rollback", "health":
		default:
			return errors.New("unsupported workflow action")
		}
		// Docker stores may identify a pulled image by its configuration or by
		// the exact manifest digest. The only alternative is the reviewed digest
		// itself, and only cache preparation may use this bounded alternative.
		if len(step.ImageIDAlternatives) > 1 {
			return errors.New("invalid image identity alternatives")
		}
		for _, id := range step.ImageIDAlternatives {
			if c.Action != "prepare-image" || !validRuntimeImageID(id) || !strings.HasSuffix(c.Image, "@"+id) || id == step.ImageID {
				return errors.New("invalid image identity alternative")
			}
		}
		c.Version = 1
		c.ID = "validation"
		c.ExpiresAt = time.Now().Add(time.Hour)
		c.SyncImageEnv = false
		c.InventoryRevision = ""
		c.PreviousImageID = ""
		if c.Validate() != nil {
			return errors.New("invalid workflow command")
		}
		if (c.Action == "deploy" || c.Action == "rollback" || c.Action == "prepare-image") && !validRuntimeImageID(step.ImageID) {
			return errors.New("immutable image identity required")
		}
	}
	return nil
}
func (s *Server) workflowPreview(c *gin.Context) {
	var req workflowInput
	if c.ShouldBindJSON(&req) != nil || !control.Identifier.MatchString(req.RequestID) {
		c.Status(400)
		return
	}
	provider, ok := s.workflowProvider(c, req.Provider, false)
	if !ok {
		return
	}
	plan, err := provider.Resolve(c.Request.Context(), s.db, req.RequestID, req.Intent)
	if err == nil {
		err = validWorkflowPlan(plan)
	}
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"plan": plan, "preview_hash": planHash(plan)})
}
func (s *Server) workflowCreate(c *gin.Context) {
	var req workflowInput
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if !control.Identifier.MatchString(req.RequestID) {
		c.Status(400)
		return
	}
	provider, ok := s.workflowProvider(c, req.Provider, false)
	if !ok {
		return
	}
	s.change(c, body, "workflow.create", req.RequestID, func(tx *gorm.DB) (any, error) {
		var old Workflow
		var canonical any
		decoder := json.NewDecoder(bytes.NewReader(req.Intent))
		decoder.UseNumber()
		if decoder.Decode(&canonical) != nil {
			return nil, errConflict
		}
		intentJSON, _ := json.Marshal(canonical)
		hash := control.Digest(append([]byte(req.Provider+"\n"), intentJSON...))
		err := tx.First(&old, "id = ?", req.RequestID).Error
		if err == nil {
			if old.RequestedBy != uint32(c.GetUint("admin_id")) || old.RequestHash != hash {
				return nil, errConflict
			}
			return unchangedMutation{old}, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		plan, err := provider.Resolve(c.Request.Context(), tx, req.RequestID, req.Intent)
		if err != nil {
			return nil, err
		}
		if validWorkflowPlan(plan) != nil || planHash(plan) != req.PreviewHash {
			return nil, errConflict
		}
		raw, _ := json.Marshal(plan)
		now := time.Now().UTC()
		w := Workflow{ID: req.RequestID, Provider: req.Provider, Group: plan.Group, Kind: plan.Kind, RequestHash: hash, Plan: string(raw), Status: "pending", RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(2 * time.Hour)}
		if plan.CacheOnly {
			w.Status = "running"
			if err = s.lockWorkflow(tx, w, plan); err != nil {
				return nil, err
			}
		}
		if err = tx.Create(&w).Error; err != nil {
			return nil, err
		}
		return w, nil
	})
}
func (s *Server) workflowReview(c *gin.Context) {
	var row Workflow
	if s.db.First(&row, "id = ?", c.Param("id")).Error != nil {
		c.Status(404)
		return
	}
	provider, ok := s.workflowProvider(c, row.Provider, true)
	if !ok {
		return
	}
	s.change(c, nil, "workflow.review", row.ID, func(tx *gorm.DB) (any, error) {
		var w Workflow
		if tx.First(&w, "id = ?", row.ID).Error != nil || w.Status != "pending" {
			return nil, errConflict
		}
		if c.Param("decision") == "reject" {
			w.Status = "rejected"
			w.UpdatedAt = time.Now().UTC()
			return w, tx.Save(&w).Error
		}
		if c.Param("decision") != "approve" || w.RequestedBy == uint32(c.GetUint("admin_id")) || time.Now().After(w.ExpiresAt) {
			return nil, errConflict
		}
		var p WorkflowPlan
		if json.Unmarshal([]byte(w.Plan), &p) != nil || validWorkflowPlan(p) != nil {
			return nil, errConflict
		}
		if err := s.checkWorkflowScope(tx, w, p, true); err != nil {
			return nil, err
		}
		if provider.Validate != nil {
			if err := provider.Validate(c.Request.Context(), tx, p); err != nil {
				return nil, err
			}
		}
		if err := s.lockWorkflow(tx, w, p); err != nil {
			return nil, err
		}
		w.ApprovedBy = uint32(c.GetUint("admin_id"))
		w.Status = "running"
		w.UpdatedAt = time.Now().UTC()
		return w, tx.Save(&w).Error
	})
}
func (s *Server) lockWorkflow(tx *gorm.DB, w Workflow, p WorkflowPlan) error {
	resources := []string{"group:" + p.Group}
	for _, t := range p.Targets {
		var r ServiceRecord
		if tx.First(&r, "id = ?", t.Service).Error != nil || r.BusyTask != "" {
			return errConflict
		}
		resources = append(resources, "service:"+t.Service)
	}
	sort.Strings(resources)
	for _, key := range resources {
		var l WorkflowLock
		err := tx.First(&l, "resource = ?", key).Error
		if err == nil && l.WorkflowID != w.ID {
			return errConflict
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err != nil {
			if err = tx.Create(&WorkflowLock{Resource: key, WorkflowID: w.ID}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
func workflowLocked(tx *gorm.DB, service, owner string) bool {
	var row WorkflowLock
	err := tx.First(&row, "resource = ?", "service:"+service).Error
	return err != nil && !errors.Is(err, gorm.ErrRecordNotFound) || err == nil && row.WorkflowID != owner
}
func (s *Server) workflowsList(c *gin.Context) {
	var rows []Workflow
	if s.db.Order("created_at desc").Limit(100).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) workflowDetail(c *gin.Context) {
	var w Workflow
	if s.db.First(&w, "id = ?", c.Param("id")).Error != nil {
		c.Status(404)
		return
	}
	var tasks []Task
	if s.db.Where("workflow_id = ?", w.ID).Order("created_at,id").Find(&tasks).Error != nil {
		c.Status(503)
		return
	}
	var plan WorkflowPlan
	if json.Unmarshal([]byte(w.Plan), &plan) != nil {
		c.Status(503)
		return
	}
	active := plan.Steps
	if w.RollingBack {
		active = plan.Rollback
	}
	progress := []gin.H{}
	for i, step := range active {
		id := workflowStepID(w, i)
		status := "pending"
		code := ""
		if i < w.Position {
			status = "skipped"
		}
		for _, t := range tasks {
			if t.ID == id {
				status = t.Status
				var result control.Result
				json.Unmarshal([]byte(t.Result), &result)
				code = result.Code
				break
			}
		}
		progress = append(progress, gin.H{"index": i, "label": step.Label, "task_id": id, "status": status, "code": code})
	}
	c.JSON(200, gin.H{"workflow": w, "tasks": tasks, "steps": progress})
}
func workflowObserved(tx *gorm.DB, t WorkflowTarget) (control.Observation, error) {
	var host Host
	if tx.First(&host, "id = ?", t.Host).Error != nil || !host.Active || time.Since(host.LastSeen) > time.Minute {
		return control.Observation{}, errors.New("host offline")
	}
	var observations []control.Observation
	if json.Unmarshal([]byte(host.Observations), &observations) != nil {
		return control.Observation{}, errors.New("inventory unavailable")
	}
	for _, o := range observations {
		if o.Service == t.Service {
			return o, nil
		}
	}
	return control.Observation{}, errors.New("service observation missing")
}
func (s *Server) checkWorkflowScope(tx *gorm.DB, w Workflow, p WorkflowPlan, initial bool) error {
	expected := map[string]WorkflowTarget{}
	for _, t := range p.Targets {
		expected[t.Service] = t
	}
	if !initial {
		var tasks []Task
		if tx.Where("workflow_id = ? AND action IN ?", w.ID, []string{"deploy", "rollback"}).Order("created_at,id").Find(&tasks).Error != nil {
			return errConflict
		}
		for _, task := range tasks {
			var command control.Command
			var result control.Result
			json.Unmarshal([]byte(task.Payload), &command)
			json.Unmarshal([]byte(task.Result), &result)
			if task.Status == "succeeded" {
				t := expected[task.ServiceID]
				t.Image = command.Image
				var steps = []WorkflowStep{}
				steps = append(steps, p.Steps...)
				steps = append(steps, p.Rollback...)
				for _, step := range steps {
					if step.Command.Service == task.ServiceID && step.Command.Image == command.Image {
						t.ImageID = step.ImageID
						break
					}
				}
				expected[task.ServiceID] = t
			}
		}
	}
	for _, t := range p.Targets {
		var record ServiceRecord
		if tx.First(&record, "id = ?", t.Service).Error != nil || record.Definition != t.Definition {
			return errors.New("service definition changed")
		}
		o, err := workflowObserved(tx, t)
		if err != nil {
			return err
		}
		want := expected[t.Service]
		if o.ScopeRevision != t.ScopeRevision || o.ConfiguredImage != want.Image || o.ImageID != want.ImageID {
			return errors.New("execution scope changed")
		}
	}
	return nil
}
func (s *Server) workflowTick(ctx context.Context) error {
	var rows []Workflow
	if s.db.WithContext(ctx).Where("status IN ?", []string{"running", "waiting", "blocked", "rolling_back"}).Find(&rows).Error != nil {
		return errConflict
	}
	for _, row := range rows {
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var lock AuditLock
			if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error != nil {
				return errConflict
			}
			var w Workflow
			if tx.First(&w, "id = ?", row.ID).Error != nil {
				return errConflict
			}
			var p WorkflowPlan
			if json.Unmarshal([]byte(w.Plan), &p) != nil || validWorkflowPlan(p) != nil {
				return errConflict
			}
			save := func() error { w.UpdatedAt = time.Now().UTC(); return tx.Save(&w).Error }
			if w.ChildID != "" {
				var child Task
				if tx.First(&child, "id = ?", w.ChildID).Error != nil {
					return errConflict
				}
				if child.Status == "queued" || child.Status == "dispatched" || child.Status == "uncertain" {
					w.Status = "waiting"
					if child.Status == "uncertain" {
						w.Code = "RESULT_UNCERTAIN"
					}
					return save()
				}
				var result control.Result
				json.Unmarshal([]byte(child.Result), &result)
				activeSteps := p.Steps
				if w.RollingBack {
					activeSteps = p.Rollback
				}
				identityBad := w.Position >= len(activeSteps)
				if !identityBad && child.Action == "prepare-image" {
					identityBad = !workflowPreparedIdentity(activeSteps[w.Position], result)
				}
				if child.Status != "succeeded" || identityBad || (child.Action == "health" && !result.Healthy) {
					if len(p.Rollback) > 0 && !w.RollingBack {
						w.RollingBack = true
						w.Position = 0
						w.ChildID = ""
						w.Status = "rolling_back"
						w.Code = "RECOVERING"
						return save()
					}
					w.Status = "blocked"
					w.Code = "STEP_FAILED:" + result.Code
					return save()
				}
				// Wait for a heartbeat produced after the terminal receipt. In-flight
				// observations must not be mistaken for external image/config drift.
				var host Host
				if tx.First(&host, "id = ?", child.HostID).Error != nil || child.FinishedAt != nil && host.LastSeen.Before(child.FinishedAt.Add(5*time.Second)) {
					return nil
				}
				w.Position++
				w.ChildID = ""
				w.Code = ""
				w.Status = "running"
			}
			steps := p.Steps
			if w.RollingBack {
				steps = p.Rollback
			}
			if w.Position >= len(steps) {
				if s.checkWorkflowScope(tx, w, p, false) != nil {
					w.Status = "blocked"
					w.Code = "FINAL_SCOPE_UNVERIFIED"
					return save()
				}
				w.Status = "succeeded"
				if w.RollingBack {
					w.Status = "rolled_back"
				}
				w.Code = "VERIFIED"
				if tx.Where("workflow_id = ?", w.ID).Delete(&WorkflowLock{}).Error != nil {
					return errConflict
				}
				return save()
			}
			if time.Now().After(w.ExpiresAt) {
				w.Status = "blocked"
				w.Code = "AUTHORIZATION_EXPIRED"
				return save()
			}
			provider, ok := s.cfg.Workflows[w.Provider]
			if !ok {
				return errConflict
			}
			var requester AdminUser
			if tx.First(&requester, w.RequestedBy).Error != nil || !requester.Active {
				w.Status = "blocked"
				w.Code = "REQUESTER_UNAVAILABLE"
				return save()
			}
			allowed, _ := s.auth.Enforce(requester.Role, provider.Permission)
			coreAllowed, _ := s.auth.Enforce(requester.Role, "ops.write")
			if !allowed || !coreAllowed {
				w.Status = "blocked"
				w.Code = "AUTHORIZATION_CHANGED"
				return save()
			}
			if !p.CacheOnly {
				var reviewer AdminUser
				if tx.First(&reviewer, w.ApprovedBy).Error != nil || !reviewer.Active || w.ApprovedBy == w.RequestedBy {
					w.Status = "blocked"
					w.Code = "APPROVER_UNAVAILABLE"
					return save()
				}
				allowed, _ = s.auth.Enforce(reviewer.Role, provider.ApprovalPermission)
				coreAllowed, _ = s.auth.Enforce(reviewer.Role, "ops.approve")
				if !allowed || !coreAllowed {
					w.Status = "blocked"
					w.Code = "AUTHORIZATION_CHANGED"
					return save()
				}
			}
			if err := s.checkWorkflowScope(tx, w, p, false); err != nil {
				w.Status = "blocked"
				w.Code = "SCOPE_OR_HOST_CHANGED"
				return save()
			}
			if provider.Validate != nil {
				if err := provider.Validate(ctx, tx, p); err != nil {
					w.Status = "blocked"
					w.Code = "TRUST_OR_POLICY_CHANGED"
					return save()
				}
			}
			step := steps[w.Position]
			var target WorkflowTarget
			for _, t := range p.Targets {
				if t.Service == step.Command.Service {
					target = t
				}
			}
			o, err := workflowObserved(tx, target)
			if err != nil {
				return err
			}
			if step.When == "running" && !o.Running || step.When == "stopped" && o.Running {
				w.Position++
				return save()
			}
			command := step.Command
			command.Version = 1
			command.ID = workflowStepID(w, w.Position)
			command.WorkflowID = w.ID
			command.WorkflowScopeRevision = o.ScopeRevision
			command.WorkflowImage = o.ConfiguredImage
			command.PreviousImageID = o.ImageID
			command.HostID = target.Host
			command.ExpiresAt = w.ExpiresAt
			if command.Action == "deploy" || command.Action == "rollback" {
				command.SyncImageEnv = o.SyncImageEnv
				command.InventoryRevision = o.InventoryRevision
				command.PreviousImageID = o.ImageID
			}
			if command.Validate() != nil {
				return errConflict
			}
			if s.checkExtensionTask(tx, command, w.RequestedBy, w.ApprovedBy) != nil {
				w.Status = "blocked"
				w.Code = "EXTENSION_AUTHORIZATION_CHANGED"
				return save()
			}
			if s.cfg.TaskPolicy != nil && s.cfg.TaskPolicy(ctx, tx, command) != nil {
				w.Status = "blocked"
				w.Code = "COMPOSITION_POLICY_CHANGED"
				return save()
			}
			var record ServiceRecord
			if tx.First(&record, "id = ?", target.Service).Error != nil || record.BusyTask != "" {
				return errConflict
			}
			raw, _ := json.Marshal(command)
			child := Task{ID: command.ID, WorkflowID: w.ID, HostID: target.Host, ServiceID: target.Service, Action: command.Action, Payload: string(raw), PayloadHash: control.Digest(raw), Status: "queued", RequestedBy: w.RequestedBy, ApprovedBy: w.ApprovedBy, CreatedAt: time.Now().UTC(), ExpiresAt: w.ExpiresAt}
			if err = tx.Create(&child).Error; err != nil {
				return err
			}
			if tx.Model(&record).Update("busy_task", child.ID).Error != nil {
				return errConflict
			}
			if err := s.appendAudit(tx, 0, "workflow.step.queued", child.ID, nil); err != nil {
				return err
			}
			w.ChildID = child.ID
			w.Status = "waiting"
			return save()
		}); err != nil {
			return err
		}
	}
	return nil
}

// Dispatch checks happen again immediately before signing; a queued command is
// not permission to execute after its environment or authority has changed.
func (s *Server) workflowDispatch(tx *gorm.DB, child Task) error {
	var w Workflow
	if tx.First(&w, "id = ?", child.WorkflowID).Error != nil || w.ChildID != child.ID || w.Status != "waiting" || time.Now().After(w.ExpiresAt) {
		return errConflict
	}
	var p WorkflowPlan
	if json.Unmarshal([]byte(w.Plan), &p) != nil || validWorkflowPlan(p) != nil {
		return errConflict
	}
	provider, ok := s.cfg.Workflows[w.Provider]
	if !ok {
		return errConflict
	}
	for _, target := range p.Targets {
		if workflowLocked(tx, target.Service, w.ID) {
			return errConflict
		}
	}
	if err := s.checkWorkflowScope(tx, w, p, false); err != nil {
		return err
	}
	for _, identity := range []struct {
		id         uint32
		permission string
	}{{w.RequestedBy, provider.Permission}, {w.RequestedBy, "ops.write"}, {w.ApprovedBy, provider.ApprovalPermission}, {w.ApprovedBy, "ops.approve"}} {
		if p.CacheOnly && identity.id == 0 {
			continue
		}
		var user AdminUser
		if tx.First(&user, identity.id).Error != nil || !user.Active {
			return errConflict
		}
		allowed, _ := s.auth.Enforce(user.Role, identity.permission)
		if !allowed {
			return errConflict
		}
	}
	if provider.Validate != nil {
		return provider.Validate(tx.Statement.Context, tx, p)
	}
	return nil
}

func workflowStepID(w Workflow, position int) string {
	return "wf-" + control.Digest([]byte(fmt.Sprintf("%s/%t/%d", w.ID, w.RollingBack, position)))[:48]
}

func workflowPreparedIdentity(step WorkflowStep, result control.Result) bool {
	if result.Image != step.Command.Image || result.Platform != step.Command.Platform {
		return false
	}
	if result.ImageID == step.ImageID {
		return true
	}
	for _, id := range step.ImageIDAlternatives {
		if result.ImageID == id {
			return true
		}
	}
	return false
}
