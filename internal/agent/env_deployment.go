package agent

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"os"
	"path/filepath"
	"time"
)

type envDeployment struct {
	Envelope control.Envelope
	Phase    string
	Result   control.Result
}

func (a *Agent) recoverEnvDeployments(ctx context.Context) {
	paths, _ := filepath.Glob(filepath.Join(a.cfg.StateDir, "*.deploy.json"))
	for _, path := range paths {
		var j envDeployment
		b, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(b, &j) == nil {
			a.Handle(ctx, j.Envelope)
		}
	}
}
func (a *Agent) handleEnvDeployment(ctx context.Context, e control.Envelope, c control.Command, s LocalService) control.Result {
	r := control.Result{ID: c.ID, Status: "uncertain", Code: "ENV_RECOVERY_REQUIRED"}
	path := filepath.Join(a.cfg.StateDir, c.ID+".deploy.json")
	j := envDeployment{Envelope: e, Phase: "preflight", Result: r}
	recovering := false
	save := func() error { b, _ := json.Marshal(j); return settings.Atomic(path, b) }
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &j) != nil || string(j.Envelope.Payload) != string(e.Payload) || string(j.Envelope.Signature) != string(e.Signature) {
			return r
		}
		if j.Phase == "terminal" {
			return j.Result
		}
		recovering = true
	} else if !os.IsNotExist(err) {
		return r
	}
	finish := func(result control.Result) control.Result {
		if (result.Status == "succeeded" || result.Code == "DEPLOYMENT_ROLLED_BACK") && !a.envMatches(c.ID, result.Status == "succeeded") {
			return r
		}
		j.Phase = "terminal"
		j.Result = result
		if save() != nil {
			return r
		}
		return result
	}
	if !s.SyncImageEnv || !c.SyncImageEnv {
		return finish(control.Result{ID: c.ID, Status: "failed", Code: "ENV_NOT_AUTHORIZED"})
	}
	revision, err := a.revisionForTask(s, c.ID)
	if err != nil || revision != c.InventoryRevision {
		if recovering {
			return r
		}
		return finish(control.Result{ID: c.ID, Status: "failed", Code: "ENV_SCOPE_CHANGED"})
	}
	d, ok := a.driver.(unitDriver)
	if !ok {
		return r
	}
	work, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if recovering && j.Phase == "deploying" {
		o, err := a.driver.Observe(work, s)
		image, e := preparedImage(work, s, c.Image)
		if err == nil && e == nil && o.Healthy && o.ImageID == image.ID && a.envMatches(c.ID, true) {
			return finish(control.Result{ID: c.ID, Status: "succeeded", Code: "VERIFIED", Image: c.Image, Healthy: true})
		}
	}
	if !recovering {
		if a.cfg.Guard != nil {
			if err := a.cfg.Guard(work, s, c); err != nil {
				return finish(control.Result{ID: c.ID, Status: "failed", Code: "LOCAL_POLICY_REJECTED"})
			}
		}
		o, err := a.driver.Observe(work, s)
		if err != nil || (o.Running && !o.Healthy) || o.ImageID != c.PreviousImageID || time.Now().After(c.ExpiresAt) {
			return finish(control.Result{ID: c.ID, Status: "failed", Code: "ENV_SCOPE_CHANGED"})
		}
		// Cache-only preparation precedes the journaled service mutation.
		prepared := a.driver.Execute(work, s, control.Command{ID: c.ID, Action: "prepare-image", Image: c.Image, Platform: o.Platform})
		if prepared.Status != "succeeded" {
			prepared.ID = c.ID
			return finish(prepared)
		}
		if err := a.planEnv(work, c.ID, []LocalService{s}, []string{c.Image}); err != nil {
			return finish(control.Result{ID: c.ID, Status: "failed", Code: "ENV_PREFLIGHT_REJECTED"})
		}
		if revision, err := a.revisionForTask(s, c.ID); err != nil || revision != c.InventoryRevision {
			return finish(control.Result{ID: c.ID, Status: "failed", Code: "ENV_SCOPE_CHANGED"})
		}
		if a.cfg.Guard != nil {
			if err := a.cfg.Guard(work, s, c); err != nil {
				return finish(control.Result{ID: c.ID, Status: "failed", Code: "LOCAL_POLICY_REJECTED"})
			}
		}
		j.Phase = "writing"
		if save() != nil {
			return r
		}
		if a.applyEnv(c.ID, false) == nil {
			j.Phase = "deploying"
			if save() != nil {
				return r
			}
			if revision, err := a.revisionForTask(s, c.ID); err != nil || revision != c.InventoryRevision {
				return r
			}
			result := a.driver.Execute(work, s, c)
			if result.Status == "succeeded" {
				result.ID = c.ID
				return finish(result)
			}
		}
	}
	if ctx.Err() != nil {
		return r
	}
	rollback, done := context.WithTimeout(ctx, 2*time.Minute)
	defer done()
	j.Phase = "rollback"
	if save() != nil {
		return r
	}
	if revision, err := a.revisionForTask(s, c.ID); err != nil || revision != c.InventoryRevision {
		return r
	}
	if d.UnitStop(rollback, s) != nil || a.applyEnv(c.ID, true) != nil || d.UnitStart(rollback, s, c.PreviousImageID) != nil {
		return r
	}
	for {
		o, err := a.driver.Observe(rollback, s)
		if err == nil && o.Healthy && o.ImageID == c.PreviousImageID {
			return finish(control.Result{ID: c.ID, Status: "failed", Code: "DEPLOYMENT_ROLLED_BACK"})
		}
		select {
		case <-rollback.Done():
			return r
		case <-time.After(time.Second):
		}
	}
}
