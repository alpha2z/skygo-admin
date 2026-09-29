package app

import (
	"context"
	"github.com/alpha2z/skygo-admin/internal/githubbuild"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type registrationRetry struct {
	Next  time.Time
	Delay time.Duration
}

func (r registrationRetry) failed(now time.Time) registrationRetry {
	if r.Delay == 0 {
		r.Delay = 30 * time.Second
	} else {
		r.Delay *= 2
	}
	if r.Delay > 8*time.Minute {
		r.Delay = 8 * time.Minute
	}
	r.Next = now.Add(r.Delay)
	return r
}
func (s *Server) autoRegisterLoop(ctx context.Context) {
	if s.github == nil || !s.github.Config().AutoRegister {
		return
	}
	retries := map[string]registrationRetry{}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 25*time.Second)
		s.autoRegisterCycle(work, retries, time.Now().UTC())
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) autoRegisterCycle(ctx context.Context, retries map[string]registrationRetry, now time.Time) {
	if s.github == nil || !s.github.Config().AutoRegister {
		return
	}
	if _, err := s.buildKeys(s.db.WithContext(ctx)); err != nil {
		return
	}
	client, ok := s.github.(interface {
		SyncRuns(context.Context) ([]githubbuild.Run, error)
	})
	if !ok {
		return
	}
	runs, err := client.SyncRuns(ctx)
	if err != nil {
		return
	}
	if len(runs) > 150 {
		runs = runs[:150]
	}
	states := s.registrations(runs, s.github.Config())
	live := map[string]bool{}
	for _, run := range runs {
		live[registrationID(run)] = true
	}
	for id := range retries {
		if !live[id] {
			delete(retries, id)
		}
	}
	verified := 0
	for _, run := range runs {
		id := registrationID(run)
		if verified >= 3 || ctx.Err() != nil {
			break
		}
		if states[id].Status != "unregistered" || run.Attempt < 1 || run.Status != "completed" || run.Conclusion == nil || *run.Conclusion != "success" || (run.Event != "push" && run.Event != "workflow_dispatch") || now.Before(retries[id].Next) {
			continue
		}
		verified++
		artifact, err := s.github.SignedImages(ctx, run.ID)
		if err == nil {
			err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var lock AuditLock
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error; err != nil {
					return err
				}
				result, err := s.persistBuild(tx, artifact, run.ID, run.Attempt)
				if err != nil {
					return err
				}
				if _, unchanged := result.(unchangedMutation); unchanged {
					return nil
				}
				return s.appendAudit(tx, 0, "system.build.register", id, result)
			})
		}
		if err != nil {
			retries[id] = retries[id].failed(now)
		} else {
			delete(retries, id)
		}
	}
}
