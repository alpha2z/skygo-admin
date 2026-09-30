package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"path/filepath"
	"testing"
	"time"
)

func TestExtensionUsesSignedJournalAndLocalAllowlist(t *testing.T) {
	a, key, d := fixture(t)
	executions, reconciliations := 0, 0
	action := ExtensionAction{Validate: func(b json.RawMessage) error {
		if string(b) != `{"mode":"check"}` {
			return errors.New("invalid scope")
		}
		return nil
	}, Execute: func(context.Context, LocalService, control.Command) control.Result {
		executions++
		return control.Result{Status: "succeeded"}
	}, Reconcile: func(context.Context, LocalService, control.Command) control.Result {
		reconciliations++
		return control.Result{Status: "uncertain"}
	}}
	a.cfg.Extensions = map[string]ExtensionAction{"sample": action}
	cmd := control.Command{Version: 1, ID: "ext-a", HostID: "host-a", Service: "worker", Action: "extension", Extension: "sample", Payload: json.RawMessage(`{"mode":"check"}`), ExpiresAt: time.Now().Add(time.Minute)}
	signed, err := control.Sign(cmd, key)
	if err != nil {
		t.Fatal(err)
	}
	if a.Handle(context.Background(), signed).Status != "failed" || executions != 0 {
		t.Fatal("local authorization bypass")
	}
	a.cfg.Services[0].Extensions = []string{"sample"}
	if a.Handle(context.Background(), signed).Status != "succeeded" || executions != 1 {
		t.Fatal("extension not executed")
	}
	fresh, err := New(a.cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Handle(context.Background(), signed).Status != "succeeded" || executions != 1 {
		t.Fatal("replay executed twice")
	}
	cmd.ID = "ext-interrupted"
	signed, _ = control.Sign(cmd, key)
	if a.save(filepath.Join(a.cfg.StateDir, cmd.ID+".json"), receipt{Digest: control.Digest(signed.Payload), Command: cmd, Result: control.Result{ID: cmd.ID, Status: "uncertain"}}) != nil {
		t.Fatal("fixture")
	}
	if a.Handle(context.Background(), signed).Status != "uncertain" || executions != 1 || reconciliations != 1 {
		t.Fatal("interruption repeated forward execution")
	}
	signed.Payload = append(signed.Payload, ' ')
	a.Handle(context.Background(), signed)
	if executions != 1 {
		t.Fatal("tampered command executed")
	}
}

func TestLocalPolicyCannotChangeTerminalReceipt(t *testing.T) {
	a, key, d := fixture(t)
	cmd := command()
	signed, _ := control.Sign(cmd, key)
	if a.Handle(context.Background(), signed).Status != "succeeded" {
		t.Fatal("fixture execution")
	}
	a.cfg.Guard = func(context.Context, LocalService, control.Command) error { return errors.New("changed local policy") }
	if a.Handle(context.Background(), signed).Status != "succeeded" || d.calls != 1 {
		t.Fatal("terminal replay reevaluated live policy")
	}
	cmd.ID = "policy-rejected"
	signed, _ = control.Sign(cmd, key)
	if a.Handle(context.Background(), signed).Code != "LOCAL_POLICY_REJECTED" {
		t.Fatal("policy bypass")
	}
	a.cfg.Guard = nil
	if a.Handle(context.Background(), signed).Code != "LOCAL_POLICY_REJECTED" || d.calls != 1 {
		t.Fatal("rejected intent executed after a policy change")
	}
}
