package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LocalUnit struct {
	ID  string `json:"id"`
	API string `json:"api"`
	Web string `json:"web"`
}
type unitDriver interface {
	UnitImage(context.Context, LocalService, string, string, string) error
	UnitStop(context.Context, LocalService) error
	UnitStart(context.Context, LocalService, string) error
}
type unitJournal struct {
	Envelope control.Envelope `json:"envelope"`
	Phase    string           `json:"phase"`
	Result   control.Result   `json:"result"`
}

func validateLocalUnit(c Config) error {
	u := c.ControlUnit
	if u == nil {
		return nil
	}
	if !control.Identifier.MatchString(u.ID) || u.API == u.Web {
		return errors.New("invalid local recovery unit")
	}
	members := map[string]LocalService{}
	for _, s := range c.Services {
		members[s.ID] = s
	}
	for _, id := range []string{u.API, u.Web} {
		s, ok := members[id]
		if !ok || s.HealthURL == "" || s.ObservationOnly {
			return errors.New("unit members require local health checks")
		}
	}
	a, w := members[u.API], members[u.Web]
	if a.Project == w.Project && a.ComposeService == w.ComposeService {
		return errors.New("unit members must be distinct containers")
	}
	return nil
}

// HMAC prevents credentials in local configuration from becoming guessable hashes.
func (a *Agent) unitRevision(s LocalService) (string, error) { return a.unitRevisionWithEnv(s, nil) }
func (a *Agent) unitRevisionWithEnv(s LocalService, overrides map[string][]byte) (string, error) {
	mac := hmac.New(sha256.New, []byte(a.token))
	b, _ := json.Marshal(struct {
		Service LocalService
		Unit    *LocalUnit
	}{s, a.cfg.ControlUnit})
	mac.Write(b)
	for _, path := range []string{s.ComposeFile, s.EnvFile, s.ConfigPath} {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return "", errors.New("unit configuration unavailable")
		}
		b, err := io.ReadAll(io.LimitReader(f, 2<<20+1))
		f.Close()
		if err != nil || len(b) > 2<<20 {
			return "", errors.New("unit configuration unavailable")
		}
		if value, ok := overrides[path]; ok {
			b = value
		}
		size, _ := json.Marshal(len(b))
		mac.Write(size)
		mac.Write(b)
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func (a *Agent) decorateUnit(s LocalService, o *control.Observation) {
	u := a.cfg.ControlUnit
	if u == nil {
		return
	}
	if _, ok := a.driver.(unitDriver); !ok {
		return
	}
	kind := ""
	if s.ID == u.API {
		kind = "admin-api"
	}
	if s.ID == u.Web {
		kind = "admin-web"
	}
	if kind == "" {
		return
	}
	revision, err := a.unitRevision(s)
	if err != nil {
		return
	}
	o.UnitID = u.ID
	o.ControlKind = kind
	o.InventoryRevision = revision
	o.Capabilities = append(o.Capabilities, "control.unit.v1")
}
func (a *Agent) unitServices(p *control.UnitPlan, taskID ...string) ([]LocalService, error) {
	u := a.cfg.ControlUnit
	if u == nil || u.ID != p.ID {
		return nil, errors.New("unit not approved locally")
	}
	out := []LocalService{}
	for _, t := range p.Targets {
		if (t.Kind == "admin-api" && t.Service != u.API) || (t.Kind == "admin-web" && t.Service != u.Web) {
			return nil, errors.New("unit membership changed")
		}
		found := false
		for _, s := range a.cfg.Services {
			if s.ID == t.Service {
				rev, err := a.unitRevision(s)
				if len(taskID) > 0 {
					rev, err = a.revisionForTask(s, taskID[0])
				}
				if err != nil || rev != t.Revision || s.SyncImageEnv != t.SyncImageEnv {
					return nil, errors.New("unit configuration changed")
				}
				out = append(out, s)
				found = true
			}
		}
		if !found {
			return nil, errors.New("unit member unavailable")
		}
	}
	return out, nil
}
func (a *Agent) saveUnit(path string, j unitJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return settings.Atomic(path, b)
}
func (a *Agent) recoverUnits(ctx context.Context) {
	paths, err := filepath.Glob(filepath.Join(a.cfg.StateDir, "*.unit.json"))
	if err != nil {
		return
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var j unitJournal
		if json.Unmarshal(b, &j) != nil {
			continue
		}
		a.Handle(ctx, j.Envelope)
	}
}
func (a *Agent) handleUnit(ctx context.Context, e control.Envelope, c control.Command) control.Result {
	r := control.Result{ID: c.ID, Status: "uncertain", Code: "UNIT_RECONCILIATION_REQUIRED"}
	d, ok := a.driver.(unitDriver)
	if !ok {
		r.Status = "failed"
		r.Code = "UNIT_PREFLIGHT_REJECTED"
		return r
	}
	path := filepath.Join(a.cfg.StateDir, c.ID+".unit.json")
	j := unitJournal{Envelope: e, Phase: "preflight", Result: r}
	recovering := false
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &j) != nil || control.Digest(j.Envelope.Payload) != control.Digest(e.Payload) || string(j.Envelope.Signature) != string(e.Signature) {
			return r
		}
		if j.Phase == "terminal" {
			return j.Result
		}
		recovering = true
	} else if !os.IsNotExist(err) {
		return r
	}
	services, err := a.unitServices(c.Unit, c.ID)
	if err != nil {
		if !recovering {
			r.Status = "failed"
			r.Code = "UNIT_PREFLIGHT_REJECTED"
		}
		return r
	}
	requiresEnv := false
	for _, target := range c.Unit.Targets {
		requiresEnv = requiresEnv || (target.Selected && target.SyncImageEnv)
	}
	finish := func(status, code string, proofs []control.UnitProof) control.Result {
		if requiresEnv && (status == "succeeded" || status == "rolled_back") && !a.envMatches(c.ID, status == "succeeded") {
			return r
		}
		r.Status = status
		r.Code = code
		r.Unit = proofs
		j.Result = r
		j.Phase = "terminal"
		if a.saveUnit(path, j) != nil {
			return control.Result{ID: c.ID, Status: "uncertain", Code: "JOURNAL_UNAVAILABLE"}
		}
		return r
	}
	work, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	proof := func(previous bool) ([]control.UnitProof, bool) {
		out := []control.UnitProof{}
		for i, s := range services {
			o, err := a.driver.Observe(work, s)
			expected := c.Unit.Targets[i].ImageID
			if previous {
				expected = c.Unit.Targets[i].PreviousImageID
			}
			if err != nil || !o.Healthy || o.ImageID != expected {
				return nil, false
			}
			out = append(out, control.UnitProof{Service: s.ID, ImageID: o.ImageID, Healthy: true})
		}
		return out, true
	}
	phase := func(value string) bool { j.Phase = value; return a.saveUnit(path, j) == nil }
	if recovering && (j.Phase == "starting" || j.Phase == "verifying") {
		if p, ok := proof(false); ok {
			return finish("succeeded", "UNIT_VERIFIED", p)
		}
	}
	if !recovering || j.Phase == "preflight" {
		if time.Now().After(c.ExpiresAt) || c.Unit.BootID != a.boot {
			return finish("failed", "UNIT_PREFLIGHT_REJECTED", nil)
		}
		for i, s := range services {
			t := c.Unit.Targets[i]
			o, err := a.driver.Observe(work, s)
			if err != nil || !o.Healthy || o.ImageID != t.PreviousImageID || d.UnitImage(work, s, t.Image, t.ImageID, t.Platform) != nil || d.UnitImage(work, s, t.PreviousImageID, t.PreviousImageID, t.Platform) != nil {
				return finish("failed", "UNIT_PREFLIGHT_REJECTED", nil)
			}
		}
		selectedServices := []LocalService{}
		selectedImages := []string{}
		for i, s := range services {
			if c.Unit.Targets[i].Selected && s.SyncImageEnv {
				selectedServices = append(selectedServices, s)
				selectedImages = append(selectedImages, c.Unit.Targets[i].Image)
			}
		}
		if err := a.planEnv(work, c.ID, selectedServices, selectedImages); err != nil {
			return finish("failed", "UNIT_PREFLIGHT_REJECTED", nil)
		}
		if _, err := a.unitServices(c.Unit, c.ID); err != nil {
			return finish("failed", "UNIT_PREFLIGHT_REJECTED", nil)
		}
		if !phase("stopping") {
			return r
		}
		good := true
		for i := len(services) - 1; i >= 0; i-- {
			if _, err := a.unitServices(c.Unit, c.ID); err != nil {
				return r
			}
			if d.UnitStop(work, services[i]) != nil {
				good = false
				break
			}
		}
		if good {
			if !phase("env-writing") {
				return r
			}
			if a.applyEnv(c.ID, false) != nil {
				good = false
			}
		}
		if good {
			if !phase("starting") {
				return r
			}
			for i, s := range services {
				if _, err := a.unitServices(c.Unit, c.ID); err != nil {
					return r
				}
				if d.UnitStart(work, s, c.Unit.Targets[i].Image) != nil {
					good = false
					break
				}
			}
		}
		if good {
			if !phase("verifying") {
				return r
			}
			for attempts := 0; attempts < 30; attempts++ {
				if p, ok := proof(false); ok {
					return finish("succeeded", "UNIT_VERIFIED", p)
				}
				select {
				case <-work.Done():
					attempts = 30
				case <-time.After(time.Second):
				}
			}
		}
	}
	// Once an interrupted mutation is found, never repeat the forward upgrade.
	// Persist rollback intent before any compensating operation, even with API down.
	if ctx.Err() != nil {
		return r
	}
	rollback, done := context.WithTimeout(ctx, 2*time.Minute)
	defer done()
	work = rollback
	if _, err := a.unitServices(c.Unit, c.ID); err != nil {
		return r
	}
	if !phase("rollback") {
		return r
	}
	if a.applyEnv(c.ID, true) != nil {
		return r
	}
	if p, ok := proof(true); ok {
		return finish("rolled_back", "UNIT_RESTORED", p)
	}
	for i := len(services) - 1; i >= 0; i-- {
		if _, err := a.unitServices(c.Unit, c.ID); err != nil {
			return r
		}
		if d.UnitStop(work, services[i]) != nil {
			return r
		}
	}
	for i, s := range services {
		if d.UnitStart(work, s, c.Unit.Targets[i].PreviousImageID) != nil {
			return r
		}
	}
	for attempts := 0; attempts < 30; attempts++ {
		if a.applyEnv(c.ID, true) != nil {
			return r
		}
		if p, ok := proof(true); ok {
			return finish("rolled_back", "UNIT_RESTORED", p)
		}
		select {
		case <-work.Done():
			return r
		case <-time.After(time.Second):
		}
	}
	return r
}
func (Docker) UnitImage(ctx context.Context, s LocalService, ref, id, platform string) error {
	image, err := preparedImage(ctx, s, ref)
	if err != nil || image.ID != id || image.platform() != platform {
		return errors.New("unit image unavailable")
	}
	if control.ImageIdentity(ref) {
		if ref != id {
			return errors.New("image identity mismatch")
		}
		return nil
	}
	approved := false
	for _, repository := range s.AllowedImages {
		if repository == strings.Split(ref, "@")[0] {
			approved = true
		}
	}
	if !approved || image.platform() != platform {
		return errors.New("unit image not approved")
	}
	return nil
}
func (d Docker) UnitStop(ctx context.Context, s LocalService) error {
	if _, err := run(ctx, os.Environ(), append(compose(s), "stop", "--timeout", "30", s.ComposeService)...); err != nil {
		return err
	}
	o, err := d.Observe(ctx, s)
	if err != nil || o.Running {
		return errors.New("unit stop unconfirmed")
	}
	return nil
}
func (Docker) UnitStart(ctx context.Context, s LocalService, image string) error {
	if control.Image.MatchString(image) {
		cached, err := preparedImage(ctx, s, image)
		if err != nil {
			return err
		}
		image = cached.ID
	}
	_, err := run(ctx, imageEnv(s, image), append(compose(s), "up", "-d", "--no-deps", "--pull", "never", s.ComposeService)...)
	return err
}
