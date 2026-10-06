package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/plugin"
	"gorm.io/gorm"
)

type PluginWorkflowConfig struct {
	Services           []string `json:"services"`
	Name               string   `json:"name"`
	Permission         string   `json:"permission"`
	ApprovalPermission string   `json:"approval_permission"`
	Actions            []string `json:"actions"`
}

func installPluginWorkflows(c *Config, p PluginProviderConfig) error {
	if len(p.Workflows) == 0 {
		return nil
	}
	if len(p.Services) == 0 {
		return errors.New("plugin workflow requires explicit services")
	}
	if c.Workflows == nil {
		c.Workflows = map[string]WorkflowProvider{}
	}
	permissions := map[string]bool{}
	for _, permission := range allPermissions {
		permissions[permission] = true
	}
	for _, w := range p.Workflows {
		parent := map[string]bool{}
		for _, id := range p.Services {
			parent[id] = true
		}
		seen := map[string]bool{}
		if len(w.Services) == 0 {
			return errors.New("workflow requires an explicit service scope")
		}
		for _, id := range w.Services {
			if !parent[id] || seen[id] {
				return errors.New("workflow service scope is not authorized")
			}
			seen[id] = true
		}
		p := p
		p.Services = append([]string(nil), w.Services...)
		if !control.Identifier.MatchString(w.Name) || !permissions[w.Permission] || !permissions[w.ApprovalPermission] || len(w.Actions) == 0 {
			return errors.New("invalid plugin workflow policy")
		}
		if _, exists := c.Workflows[w.Name]; exists {
			return errors.New("duplicate workflow provider")
		}
		allowed := map[string]bool{}
		for _, action := range w.Actions {
			switch action {
			case "extension", "health", "stop", "start", "deploy", "rollback", "prepare-image":
			default:
				return errors.New("invalid plugin workflow action")
			}
			allowed[action] = true
		}
		w := w
		validate := func(ctx context.Context, tx *gorm.DB, plan WorkflowPlan) error {
			if err := validatePluginWorkflow(p, allowed, plan); err != nil {
				return err
			}
			raw, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return p.Endpoint.Call(ctx, plugin.Request{Operation: "workflow-validate", Action: w.Name, Payload: raw}, nil)
		}
		c.Workflows[w.Name] = WorkflowProvider{Permission: w.Permission, ApprovalPermission: w.ApprovalPermission, Validate: validate, Resolve: func(ctx context.Context, tx *gorm.DB, id string, intent json.RawMessage) (WorkflowPlan, error) {
			var plan WorkflowPlan
			input := plugin.WorkflowPlanningRequest{RequestID: id, Provider: w.Name, Intent: intent}
			for _, service := range p.Services {
				var record ServiceRecord
				var def control.Service
				if tx.First(&record, "id = ?", service).Error != nil || json.Unmarshal([]byte(record.Definition), &def) != nil || def.PluginID != p.ID || def.ControlPlane {
					return plan, fmt.Errorf("plugin workflow service %s is not authorized", service)
				}
				o, err := workflowObserved(tx, WorkflowTarget{Host: def.HostID, Service: def.ID})
				if err != nil {
					return plan, err
				}
				if o.PluginRevision != p.Revision {
					return plan, errors.New("host plugin revision mismatch")
				}
				raw, _ := json.Marshal(o)
				input.Inventory = append(input.Inventory, plugin.WorkflowInventory{Definition: json.RawMessage(record.Definition), Observation: raw})
			}
			var preparations []Task
			if tx.Where("service_id IN ? AND action = ? AND status = ? AND finished_at >= ?", p.Services, "prepare-image", "succeeded", time.Now().Add(-5*time.Minute)).Order("finished_at DESC,id").Limit(256).Find(&preparations).Error != nil {
				return plan, errors.New("image preparation history unavailable")
			}
			for _, task := range preparations {
				var command control.Command
				var result control.Result
				if task.FinishedAt == nil || json.Unmarshal([]byte(task.Payload), &command) != nil || json.Unmarshal([]byte(task.Result), &result) != nil || result.Status != "succeeded" || !control.ImageIdentity(result.ImageID) || command.PluginRevision != p.Revision {
					continue
				}
				input.Prepared = append(input.Prepared, plugin.PreparedImage{TaskID: task.ID, Service: task.ServiceID, Host: task.HostID, Image: command.Image, ImageID: result.ImageID, Platform: result.Platform, PluginRevision: command.PluginRevision, FinishedAt: *task.FinishedAt})
			}
			raw, _ := json.Marshal(input)
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := p.Endpoint.Call(ctx, plugin.Request{Operation: "workflow-plan", Action: w.Name, Payload: raw}, &plan); err != nil {
				return plan, err
			}
			if err := validate(ctx, tx, plan); err != nil {
				return plan, err
			}
			if err := (&Server{}).checkWorkflowScope(tx, Workflow{}, plan, true); err != nil {
				return plan, err
			}
			return plan, nil
		}}
	}
	return nil
}

func validatePluginWorkflow(p PluginProviderConfig, allowed map[string]bool, plan WorkflowPlan) error {
	if err := validWorkflowPlan(plan); err != nil {
		return err
	}
	if len(plan.Targets) != len(p.Services) {
		return errors.New("plugin plan omitted authorized environment members")
	}
	scope := map[string]bool{}
	for _, id := range p.Services {
		scope[id] = true
	}
	for _, target := range plan.Targets {
		var def control.Service
		if !scope[target.Service] || target.PluginRevision != p.Revision || json.Unmarshal([]byte(target.Definition), &def) != nil || def.ID != target.Service || def.HostID != target.Host || def.PluginID != p.ID || def.ControlPlane {
			return errors.New("plugin plan exceeds local service authorization")
		}
	}
	for _, step := range append(append([]WorkflowStep{}, plan.Steps...), plan.Rollback...) {
		cmd := step.Command
		if !allowed[cmd.Action] || cmd.PluginRevision != p.Revision {
			return errors.New("plugin plan action or revision not authorized")
		}
		if cmd.Action == "extension" {
			ok := false
			for _, action := range p.Actions {
				ok = ok || action.Name == cmd.Extension
			}
			if !ok {
				return errors.New("plugin plan extension not authorized")
			}
		}
	}
	for _, step := range plan.Steps {
		if step.Command.Action != "deploy" {
			continue
		}
		found := false
		for _, receipt := range plan.Preparations {
			found = found || (receipt.Service == step.Command.Service && receipt.Host == step.Command.HostID && receipt.Image == step.Command.Image && receipt.ImageID == step.ImageID && receipt.PluginRevision == p.Revision)
		}
		if !found {
			return errors.New("plugin deployment requires a bound image preparation receipt")
		}
	}
	return nil
}
