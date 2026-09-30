package agent

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
)

// Reconcile observes or recovers an interrupted operation; it must not blindly
// repeat a mutation. The core preserves the same signed intent and service lock.
type ExtensionAction struct {
	Observe   func(context.Context, LocalService) (json.RawMessage, error)
	Validate  func(json.RawMessage) error
	Execute   func(context.Context, LocalService, control.Command) control.Result
	Reconcile func(context.Context, LocalService, control.Command) control.Result
}

func (a *Agent) extension(s LocalService, name string) (ExtensionAction, bool) {
	for _, allowed := range s.Extensions {
		if allowed == name {
			x, ok := a.cfg.Extensions[name]
			return x, ok
		}
	}
	return ExtensionAction{}, false
}
