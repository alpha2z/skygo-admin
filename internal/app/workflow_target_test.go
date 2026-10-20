package app

import (
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"strings"
	"testing"
)

func TestWorkflowTargetDiagnostics(t *testing.T) {
	for _, field := range []string{"immutable_image", "scope_revision", "runtime_image_id", "duplicate_service"} {
		t.Run(field, func(t *testing.T) {
			target := WorkflowTarget{Service: "logic", Host: "host", Image: "example/logic@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), ScopeRevision: strings.Repeat("c", 64)}
			p := WorkflowPlan{Group: "test", Reason: "test", Targets: []WorkflowTarget{target}, Steps: []WorkflowStep{{Command: control.Command{Service: "logic", HostID: "host", Action: "health"}}}}
			if err := validWorkflowPlan(p); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "immutable_image":
				p.Targets[0].Image = "example/logic:latest"
			case "scope_revision":
				p.Targets[0].ScopeRevision = ""
			case "runtime_image_id":
				p.Targets[0].ImageID = ""
			case "duplicate_service":
				p.Targets = append(p.Targets, target)
			}
			var diagnostic *workflowTargetError
			if err := validWorkflowPlan(p); !errors.As(err, &diagnostic) || diagnostic.Service != "logic" || diagnostic.Field != field {
				t.Fatalf("unexpected diagnostic: %v", err)
			}
		})
	}
}

func TestHistoricalBaselineCannotAuthorizeNewDeployment(t *testing.T) {
	target := WorkflowTarget{Service: "service", Host: "host", Image: "example/old:v1", ImageID: "sha256:" + strings.Repeat("b", 64), ScopeRevision: strings.Repeat("c", 64), HistoricalBaselineID: strings.Repeat("a", 64)}
	p := WorkflowPlan{Group: "test", Reason: "test", Targets: []WorkflowTarget{target}, Steps: []WorkflowStep{{Command: control.Command{Service: "service", HostID: "host", Action: "rollback", Image: target.Image, HistoricalBaselineID: target.HistoricalBaselineID}, ImageID: target.ImageID}}}
	if err := validWorkflowPlan(p); err != nil {
		t.Fatal(err)
	}
	p.Steps[0].Command.Action = "deploy"
	if validWorkflowPlan(p) == nil {
		t.Fatal("historical baseline accepted for new deployment")
	}
}
