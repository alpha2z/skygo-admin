package app

import (
	"crypto/ed25519"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/baseline"
)

func (s *Server) checkHistoricalBaselines(p WorkflowPlan) error {
	for _, t := range p.Targets {
		if t.HistoricalBaselineID == "" {
			continue
		}
		r, err := baseline.Read(s.cfg.HistoricalBaselinesDirectory, t.HistoricalBaselineID, s.cfg.SigningKey.Public().(ed25519.PublicKey), true)
		if err != nil || r.Host != t.Host || r.Service != t.Service || r.Tag != t.Image || r.ImageID != t.ImageID || r.ScopeRevision != t.ScopeRevision {
			return errors.New("HISTORICAL_BASELINE_UNAVAILABLE")
		}
	}
	for _, step := range append(append([]WorkflowStep{}, p.Steps...), p.Rollback...) {
		c := step.Command
		if c.HistoricalBaselineID == "" {
			continue
		}
		matched := false
		for _, t := range p.Targets {
			if c.Action == "rollback" && c.Service == t.Service && c.HostID == t.Host && c.HistoricalBaselineID == t.HistoricalBaselineID && c.Image == t.Image && step.ImageID == t.ImageID {
				matched = true
			}
		}
		if !matched {
			return errors.New("HISTORICAL_BASELINE_SCOPE_INVALID")
		}
	}
	return nil
}
