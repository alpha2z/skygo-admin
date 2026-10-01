package agent

import (
	"context"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"strings"
)

// The stable scope excludes only explicitly managed image variables. Their
// exact immutable values are reported separately and checked by the workflow.
func (a *Agent) decorateWorkflow(s LocalService, o *control.Observation) {
	if s.EnvFile == "" || s.ImageVariable == "" {
		return
	}
	raw, _, err := readEnv(s.EnvFile)
	if err != nil {
		return
	}
	line, found, err := envLine(raw, s.ImageVariable)
	if err != nil || !found {
		return
	}
	value := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
	value = strings.Trim(value, "\"'")
	if !control.Image.MatchString(value) {
		return
	}
	normalized := append([]byte{}, raw...)
	for _, member := range a.cfg.Services {
		if member.EnvFile == s.EnvFile && member.ImageVariable != "" {
			if _, _, err = envLine(raw, member.ImageVariable); err != nil {
				return
			}
			normalized = replaceLine(normalized, member.ImageVariable, member.ImageVariable+"=<managed-image>", true)
		}
	}
	revision, err := a.unitRevisionWithEnv(s, map[string][]byte{s.EnvFile: normalized})
	if err != nil {
		return
	}
	o.ScopeRevision = revision
	o.ConfiguredImage = value
	o.Capabilities = append(o.Capabilities, "workflow.scope.v1")
}

func (a *Agent) checkWorkflowLocal(ctx context.Context, s LocalService, c control.Command) error {
	if c.WorkflowScopeRevision == "" {
		return nil
	}
	o, err := a.driver.Observe(ctx, s)
	if err != nil {
		return err
	}
	a.decorateWorkflow(s, &o)
	if o.ScopeRevision != c.WorkflowScopeRevision || o.ConfiguredImage != c.WorkflowImage || o.ImageID != c.PreviousImageID {
		return errors.New("workflow local scope changed")
	}
	return nil
}
