package agent

import (
	"context"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"strings"
	"time"
)

// The stable scope excludes only explicitly managed image variables. Their
// exact immutable values are reported separately and checked by the workflow.
func (a *Agent) workflowConfiguration(s LocalService) (string, string, error) {
	if s.EnvFile == "" || s.ImageVariable == "" {
		return "", "", errors.New("workflow configuration unavailable")
	}
	raw, _, err := readEnv(s.EnvFile)
	if err != nil {
		return "", "", errors.New("workflow configuration unavailable")
	}
	line, found, err := envLine(raw, s.ImageVariable)
	if err != nil || !found {
		return "", "", errors.New("workflow configuration unavailable")
	}
	value := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
	value = strings.Trim(value, "\"'")
	normalized := append([]byte{}, raw...)
	for _, member := range a.cfg.Services {
		if member.EnvFile == s.EnvFile && member.ImageVariable != "" {
			if _, _, err = envLine(raw, member.ImageVariable); err != nil {
				return "", "", errors.New("workflow configuration unavailable")
			}
			normalized = replaceLine(normalized, member.ImageVariable, member.ImageVariable+"=<managed-image>", true)
		}
	}
	revision, err := a.unitRevisionWithEnv(s, map[string][]byte{s.EnvFile: normalized})
	if err != nil {
		return "", "", errors.New("workflow configuration unavailable")
	}
	return value, revision, nil
}
func (a *Agent) decorateWorkflow(s LocalService, o *control.Observation) {
	o.HistoricalBaselineID = ""
	o.ScopeRevision = ""
	o.ConfiguredImage = ""
	value, revision, err := a.workflowConfiguration(s)
	if err != nil {
		return
	}
	if !control.Image.MatchString(value) {
		id, r, err := a.activeBaseline(s)
		if err != nil || r.Tag != value || r.ScopeRevision != revision || r.ImageID != o.ImageID || r.Platform != o.Platform {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		image, err := inspectImage(ctx, value)
		cancel()
		if err != nil || image.ID != r.ImageID || image.platform() != r.Platform {
			return
		}
		o.HistoricalBaselineID = id
	}
	o.ScopeRevision = revision
	o.ConfiguredImage = value
	o.Capabilities = append(o.Capabilities, "workflow.scope.v1", "workflow.historical-baseline.v1")
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
	if o.HistoricalBaselineID != c.WorkflowBaselineID || o.ScopeRevision != c.WorkflowScopeRevision || o.ConfiguredImage != c.WorkflowImage || o.ImageID != c.PreviousImageID {
		return errors.New("workflow local scope changed")
	}
	return nil
}
