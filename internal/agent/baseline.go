package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alpha2z/skygo-admin/internal/baseline"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
)

func (a *Agent) baselineRecord(s LocalService, id string) (baseline.Record, error) {
	r, err := baseline.Read(filepath.Join(a.cfg.StateDir, "baselines"), id, a.key, true)
	if err != nil {
		return r, err
	}
	_, revision, err := a.workflowConfiguration(s)
	if err != nil || r.Host != a.cfg.HostID || r.Service != s.ID || r.ScopeRevision != revision {
		return r, errors.New("historical baseline scope changed")
	}
	return r, nil
}
func (a *Agent) activeBaseline(s LocalService) (string, baseline.Record, error) {
	raw, err := os.ReadFile(filepath.Join(a.cfg.StateDir, "baselines", s.ID+".active"))
	if err != nil {
		return "", baseline.Record{}, err
	}
	id := strings.TrimSpace(string(raw))
	r, err := a.baselineRecord(s, id)
	return id, r, err
}
func (a *Agent) authorizeBaseline(ctx context.Context, s LocalService, c control.Command) (LocalService, error) {
	if c.HistoricalBaselineID == "" {
		return s, nil
	}
	if c.Action != "rollback" || c.WorkflowID == "" || !c.SyncImageEnv {
		return s, errors.New("historical rollback not authorized")
	}
	r, err := a.baselineRecord(s, c.HistoricalBaselineID)
	if err != nil {
		return s, err
	}
	if r.Tag != c.Image {
		return s, errors.New("historical rollback tag changed")
	}
	// A moved tag is a conflict, not permission to overwrite it with an archive.
	if im, e := inspectImage(ctx, r.Tag); e == nil && im.ID != r.ImageID {
		return s, errors.New("historical tag now identifies different content")
	}
	archive := filepath.Join(a.cfg.StateDir, "baselines", r.ArchiveSHA256+".tar")
	im, err := inspectImage(ctx, r.ImageID)
	if err != nil {
		if _, err = run(ctx, os.Environ(), "image", "load", "--input", archive); err != nil {
			return s, err
		}
		im, err = inspectImage(ctx, r.ImageID)
	}
	if err != nil || im.ID != r.ImageID || im.platform() != r.Platform {
		return s, errors.New("historical image identity mismatch")
	}
	s.rollbackBaseline = &r
	s.baselineArchive = archive
	return s, nil
}

// CaptureBaseline reads an existing local service without starting or changing it.
func CaptureBaseline(cfg Config, service string) (baseline.Record, error) {
	if err := installPlugins(&cfg, Docker{}); err != nil {
		return baseline.Record{}, err
	}
	token, err := settings.Secret(cfg.TokenFile)
	if err != nil {
		return baseline.Record{}, err
	}
	a := Agent{cfg: cfg, token: token, driver: Docker{}}
	for _, s := range cfg.Services {
		if s.ID == service {
			value, revision, err := a.workflowConfiguration(s)
			if err != nil {
				return baseline.Record{}, err
			}
			o, err := a.driver.Observe(context.Background(), s)
			if err != nil {
				return baseline.Record{}, err
			}
			im, err := inspectImage(context.Background(), value)
			if err != nil || im.ID != o.ImageID {
				return baseline.Record{}, errors.New("configured image differs from running image")
			}
			return baseline.Record{Version: 1, Host: cfg.HostID, Service: s.ID, Tag: value, ImageID: o.ImageID, Platform: o.Platform, ScopeRevision: revision, CreatedAt: time.Now().UTC()}, nil
		}
	}
	return baseline.Record{}, errors.New("service is not in local inventory")
}

func deploymentImage(ctx context.Context, s LocalService, ref string) (imageDetails, error) {
	if s.rollbackBaseline != nil && s.rollbackBaseline.Tag == ref {
		im, err := inspectImage(ctx, s.rollbackBaseline.ImageID)
		if err != nil || im.ID != s.rollbackBaseline.ImageID || im.platform() != s.rollbackBaseline.Platform {
			return im, errors.New("historical deployment identity changed")
		}
		return im, nil
	}
	return preparedImage(ctx, s, ref)
}
