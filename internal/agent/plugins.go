package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/plugin"
	"time"
)

type PluginConfig struct {
	plugin.Endpoint
	plugin.Trust
	Actions        []string            `json:"actions"`
	Services       []string            `json:"services"`
	ServiceActions map[string][]string `json:"service_actions,omitempty"`
}

func installPlugins(cfg *Config, drivers ...Driver) error {
	observe := func(ctx context.Context, s LocalService) (json.RawMessage, error) {
		if len(drivers) != 1 || drivers[0] == nil {
			return nil, errors.New("plugin runtime observer unavailable")
		}
		o, err := drivers[0].Observe(ctx, s)
		if err != nil {
			return nil, err
		}
		o.Service = s.ID
		return json.Marshal(o)
	}
	if len(cfg.Plugins) > 16 {
		return errors.New("too many local plugins")
	}
	if cfg.Extensions == nil {
		cfg.Extensions = map[string]ExtensionAction{}
	}
	guards := map[string]plugin.Endpoint{}
	seen := map[string]bool{}
	for _, p := range cfg.Plugins {
		if p.Endpoint.Validate() != nil || seen[p.ID] || len(p.Services) == 0 || len(p.Actions) == 0 {
			return errors.New("invalid plugin inventory")
		}
		if err := p.Trust.Verify(p.Endpoint, p.Actions); err != nil {
			return err
		}
		scope, err := pluginActionScope(p)
		if err != nil {
			return err
		}
		seen[p.ID] = true
		for _, id := range p.Services {
			if _, ok := guards[id]; ok {
				return errors.New("duplicate service plugin guard")
			}
			found := false
			for i := range cfg.Services {
				if cfg.Services[i].ID == id {
					found = true
					cfg.Services[i].PluginRevision = p.Revision
				}
			}
			if !found {
				return errors.New("plugin service missing from local inventory")
			}
			guards[id] = p.Endpoint
		}
		for _, name := range p.Actions {
			if !control.Identifier.MatchString(name) {
				return errors.New("invalid plugin action")
			}
			if _, ok := cfg.Extensions[name]; ok {
				return errors.New("duplicate plugin action")
			}
			endpoint := p.Endpoint
			action := name
			authorized := map[string]bool{}
			for _, id := range p.Services {
				authorized[id] = scope[id][name]
			}
			call := func(ctx context.Context, s LocalService, c control.Command, op string) control.Result {
				if !authorized[s.ID] {
					return control.Result{ID: c.ID, Status: "failed", Code: "PLUGIN_SERVICE_NOT_AUTHORIZED"}
				}
				result := control.Result{ID: c.ID, Status: "uncertain", Code: "PLUGIN_RECOVERY_REQUIRED"}
				observation, err := observe(ctx, s)
				if err != nil {
					return result
				}
				if endpoint.Call(ctx, plugin.Request{Operation: op, Action: action, Service: s.ID, Command: mustJSON(c), Observation: observation}, &result) != nil || result.ID != c.ID {
					return control.Result{ID: c.ID, Status: "uncertain", Code: "PLUGIN_RECOVERY_REQUIRED"}
				}
				switch result.Status {
				case "succeeded", "failed", "uncertain":
				default:
					return control.Result{ID: c.ID, Status: "uncertain", Code: "PLUGIN_INVALID_RESULT"}
				}
				return result
			}
			cfg.Extensions[name] = ExtensionAction{
				Validate: func(b json.RawMessage) error {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return endpoint.Call(ctx, plugin.Request{Operation: "validate", Action: action, Payload: b}, nil)
				},
				Execute: func(ctx context.Context, s LocalService, c control.Command) control.Result {
					return call(ctx, s, c, "execute")
				},
				Reconcile: func(ctx context.Context, s LocalService, c control.Command) control.Result {
					return call(ctx, s, c, "reconcile")
				},
				Observe: func(ctx context.Context, s LocalService) (json.RawMessage, error) {
					if !authorized[s.ID] {
						return nil, errors.New("plugin service not authorized")
					}
					var data json.RawMessage
					observation, err := observe(ctx, s)
					if err != nil {
						return nil, err
					}
					err = endpoint.Call(ctx, plugin.Request{Operation: "observe", Action: action, Service: s.ID, Observation: observation}, &data)
					return data, err
				},
			}
		}
		for _, id := range p.Services {
			for _, s := range cfg.Services {
				if s.ID == id {
					for _, name := range p.Actions {
						found := false
						for _, v := range s.Extensions {
							found = found || v == name
						}
						if found != scope[id][name] {
							return errors.New("plugin action not explicitly authorized for service")
						}
					}
				}
			}
		}
	}
	old := cfg.Guard
	cfg.Guard = func(ctx context.Context, s LocalService, c control.Command) error {
		if old != nil {
			if err := old(ctx, s, c); err != nil {
				return err
			}
		}
		if endpoint, ok := guards[s.ID]; ok {
			observation, err := observe(ctx, s)
			if err != nil {
				return err
			}
			return endpoint.Call(ctx, plugin.Request{Operation: "guard", Service: s.ID, Command: mustJSON(c), Observation: observation}, nil)
		}
		return nil
	}
	return nil
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// pluginActionScope narrows each service's actions without changing legacy defaults.
func pluginActionScope(p PluginConfig) (map[string]map[string]bool, error) {
	actions := map[string]bool{}
	for _, name := range p.Actions {
		actions[name] = true
	}
	scope := map[string]map[string]bool{}
	for _, id := range p.Services {
		names := p.Actions
		if p.ServiceActions != nil {
			names = p.ServiceActions[id]
		}
		if len(names) == 0 || scope[id] != nil {
			return nil, errors.New("invalid plugin service action scope")
		}
		scope[id] = map[string]bool{}
		for _, name := range names {
			if !actions[name] || scope[id][name] {
				return nil, errors.New("invalid plugin scoped action")
			}
			scope[id][name] = true
		}
	}
	for id := range p.ServiceActions {
		if scope[id] == nil {
			return nil, errors.New("unknown plugin scoped service")
		}
	}
	return scope, nil
}
