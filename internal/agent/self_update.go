package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
)

func (a *Agent) updaterRequest(ctx context.Context, method, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", a.cfg.UpdaterSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://updater"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("updater unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("updater rejected request")
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}
func (a *Agent) updaterAvailable(ctx context.Context) bool {
	return a.cfg.UpdaterSocket != "" && a.updaterRequest(ctx, "GET", "/health", nil, nil) == nil
}
func (a *Agent) handleSelfUpdate(ctx context.Context, e control.Envelope, c control.Command) control.Result {
	r := control.Result{ID: c.ID, Status: "uncertain", Code: "UPDATER_UNAVAILABLE"}
	if c.Service != a.cfg.SelfService || a.cfg.UpdaterSocket == "" {
		r.Status = "failed"
		r.Code = "AGENT_UPDATER_REQUIRED"
		return r
	}
	if a.updaterRequest(ctx, "POST", "/update", e, &r) != nil {
		return control.Result{ID: c.ID, Status: "uncertain", Code: "UPDATER_UNAVAILABLE"}
	}
	return r
}

type updateJournal struct {
	Envelope  control.Envelope `json:"envelope"`
	Phase     string           `json:"phase"`
	Result    control.Result   `json:"result"`
	Delivered bool             `json:"delivered"`
}

// Updater runs outside the Agent container. Its only mutation target comes from
// the operator-owned self_service inventory; the wire never supplies paths.
type Updater struct {
	a         *Agent
	service   LocalService
	directory string
	mu        sync.Mutex
	server    *http.Server
	cancel    context.CancelFunc
	done      chan struct{}
	lock      *os.File
}

func NewUpdater(c Config) (*Updater, error) {
	a, err := New(c, Docker{})
	if err != nil {
		return nil, err
	}
	if c.SelfService == "" {
		return nil, errors.New("Agent updater not configured")
	}
	var service LocalService
	for _, s := range a.cfg.Services {
		if s.ID == c.SelfService {
			service = s
		}
	}
	directory := filepath.Join(c.StateDir, "updater")
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	// Keep the original inventory/cache while isolating updater journals.
	a.cfg.StateDir = directory
	return &Updater{a: a, service: service, directory: directory}, nil
}
func (u *Updater) Start(parent context.Context) error {
	f, err := os.OpenFile(filepath.Join(u.directory, "updater.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return errors.New("updater already running")
	}
	u.lock = f
	fail := func(err error) error { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close(); return err }
	socket := u.a.cfg.UpdaterSocket
	if err = os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return fail(err)
	}
	if info, e := os.Lstat(socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fail(errors.New("updater socket path occupied"))
		}
		if err = os.Remove(socket); err != nil {
			return fail(err)
		}
	} else if !os.IsNotExist(e) {
		return fail(e)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fail(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return fail(err)
	}
	ctx, cancel := context.WithCancel(parent)
	u.cancel = cancel
	u.done = make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/update", u.accept)
	u.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go u.server.Serve(listener)
	go func() {
		defer close(u.done)
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				u.tick(ctx)
			}
		}
	}()
	return nil
}
func (u *Updater) Stop(ctx context.Context) error {
	if u.cancel == nil {
		return nil
	}
	u.cancel()
	u.server.Shutdown(ctx)
	select {
	case <-u.done:
		syscall.Flock(int(u.lock.Fd()), syscall.LOCK_UN)
		return u.lock.Close()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (u *Updater) path(id string) string { return filepath.Join(u.directory, id+".update.json") }
func (u *Updater) save(j updateJournal) error {
	b, _ := json.Marshal(j)
	return settings.Atomic(u.path(j.Result.ID), b)
}
func (u *Updater) load(path string) (updateJournal, error) {
	var j updateJournal
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &j)
	}
	return j, e
}
func (u *Updater) accept(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var e control.Envelope
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&e) != nil {
		w.WriteHeader(400)
		return
	}
	c, err := control.Verify(e, u.a.key, u.a.cfg.HostID)
	if err != nil || c.Action != "upgrade-agent" || c.Service != u.service.ID {
		w.WriteHeader(403)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if old, err := u.load(u.path(c.ID)); err == nil {
		if !bytes.Equal(old.Envelope.Payload, e.Payload) || !bytes.Equal(old.Envelope.Signature, e.Signature) {
			w.WriteHeader(409)
			return
		}
		json.NewEncoder(w).Encode(old.Result)
		return
	} else if !os.IsNotExist(err) {
		w.WriteHeader(409)
		return
	}
	paths, err := filepath.Glob(filepath.Join(u.directory, "*.update.json"))
	if err != nil {
		w.WriteHeader(409)
		return
	}
	for _, path := range paths {
		j, err := u.load(path)
		if err != nil || j.Phase != "terminal" || !j.Delivered {
			w.WriteHeader(409)
			return
		}
	}
	if time.Now().After(c.ExpiresAt) {
		w.WriteHeader(409)
		return
	}
	work, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if u.preflight(work, c) != nil {
		w.WriteHeader(409)
		return
	}
	j := updateJournal{Envelope: e, Phase: "queued", Result: control.Result{ID: c.ID, Status: "uncertain", Code: "AGENT_UPDATE_HANDOFF"}}
	if u.save(j) != nil {
		w.WriteHeader(503)
		return
	}
	json.NewEncoder(w).Encode(j.Result)
}
func (u *Updater) preflight(ctx context.Context, c control.Command) error {
	if u.a.cfg.Guard != nil {
		if err := u.a.cfg.Guard(ctx, u.service, c); err != nil {
			return err
		}
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(u.directory), "*.json"))
	if err != nil {
		return errors.New("Agent journals unavailable")
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".env-journal.json") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return errors.New("Agent journal unavailable")
		}
		var state struct {
			Phase   string
			Command control.Command
			Result  control.Result
		}
		if json.Unmarshal(raw, &state) != nil {
			return errors.New("Agent journal unreadable")
		}
		if (state.Phase != "" && state.Phase != "terminal") || (state.Command.ID != "" && state.Result.Status == "uncertain") {
			return errors.New("Agent operation requires reconciliation")
		}
	}
	allowed := false
	for _, repo := range u.service.AllowedImages {
		allowed = allowed || repo == strings.Split(c.Image, "@")[0]
	}
	if !allowed {
		return errors.New("image not approved")
	}
	image, err := preparedImage(ctx, u.service, c.Image)
	if err != nil || image.ID != c.AgentUpdate.ImageID || (!image.matches(c.Image, c.Platform) && !preparedEvidence(u.service, c.Image, image.ID, c.Platform)) {
		return errors.New("prepared image unavailable")
	}
	previous, err := inspectImage(ctx, c.PreviousImageID)
	if err != nil || previous.ID != c.PreviousImageID {
		return errors.New("rollback image unavailable")
	}
	o, err := u.a.driver.Observe(ctx, u.service)
	if err != nil || !o.Running || o.ImageID != c.PreviousImageID {
		return errors.New("Agent changed")
	}
	revision, err := u.a.unitRevision(u.service)
	if err != nil || revision != c.InventoryRevision {
		return errors.New("inventory changed")
	}
	return nil
}
func (u *Updater) tick(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()
	paths, _ := filepath.Glob(filepath.Join(u.directory, "*.update.json"))
	for _, path := range paths {
		j, err := u.load(path)
		if err != nil {
			return
		}
		c, err := control.Verify(j.Envelope, u.a.key, u.a.cfg.HostID)
		if err != nil || c.Service != u.service.ID || c.Action != "upgrade-agent" || j.Result.ID != c.ID || u.path(c.ID) != path {
			return
		}
		if j.Phase != "terminal" {
			u.executeUpdate(ctx, c, &j)
		}
		if j.Phase == "terminal" && !j.Delivered {
			work, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := u.a.request(work, "POST", "/results", j.Result, nil)
			cancel()
			if err == nil {
				j.Delivered = true
				u.save(j)
			}
		}
	}
}
func (u *Updater) executeUpdate(ctx context.Context, c control.Command, j *updateJournal) {
	finish := func(status, code string) {
		j.Phase = "terminal"
		j.Result = control.Result{ID: c.ID, Status: status, Code: code}
		if status == "succeeded" {
			j.Result.Image = c.Image
			j.Result.ImageID = c.AgentUpdate.ImageID
			j.Result.Platform = c.Platform
			j.Result.Healthy = true
		}
		if u.save(*j) != nil {
			j.Phase = "unpersisted"
			j.Result.Status = "uncertain"
			j.Result.Code = "JOURNAL_UNAVAILABLE"
		}
	}
	stage := func(phase, code string) bool { j.Phase = phase; j.Result.Code = code; return u.save(*j) == nil }
	work, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	d := u.a.driver.(unitDriver)
	if j.Phase == "queued" {
		if time.Now().After(c.ExpiresAt) || u.preflight(work, c) != nil {
			finish("failed", "AGENT_UPDATE_PREFLIGHT_REJECTED")
			return
		}
		if u.a.planEnv(work, c.ID, []LocalService{u.service}, []string{c.Image}) != nil {
			finish("failed", "ENV_PREFLIGHT_REJECTED")
			return
		}
		if !stage("writing", "AGENT_UPDATE_RESTARTING") {
			return
		}
		if d.UnitStop(work, u.service) == nil && u.a.applyEnv(c.ID, false) == nil && d.UnitStart(work, u.service, c.AgentUpdate.ImageID) == nil {
			if !stage("confirming", "AGENT_UPDATE_CONFIRMING") {
				return
			}
		}
	}
	if j.Phase == "confirming" && u.waitConfirmed(work, c, c.AgentUpdate.ImageID) && u.a.envMatches(c.ID, true) {
		finish("succeeded", "AGENT_UPDATED")
		return
	}
	if ctx.Err() != nil {
		return
	}
	// Recovery never repeats the forward mutation after an interrupted write.
	revision, err := u.a.revisionForTask(u.service, c.ID)
	if err != nil || revision != c.InventoryRevision {
		j.Result.Code = "AGENT_UPDATE_RECONCILIATION_REQUIRED"
		u.save(*j)
		return
	}
	rollback, done := context.WithTimeout(ctx, 2*time.Minute)
	defer done()
	if !stage("rollback", "AGENT_UPDATE_ROLLING_BACK") {
		return
	}
	if d.UnitStop(rollback, u.service) != nil || u.a.applyEnv(c.ID, true) != nil || d.UnitStart(rollback, u.service, c.PreviousImageID) != nil {
		return
	}
	if u.waitConfirmed(rollback, c, c.PreviousImageID) && u.a.envMatches(c.ID, false) {
		finish("rolled_back", "AGENT_UPDATE_ROLLED_BACK")
	}
}
func (u *Updater) waitConfirmed(ctx context.Context, c control.Command, id string) bool {
	for {
		o, err := u.a.driver.Observe(ctx, u.service)
		if err == nil && o.Healthy && o.ImageID == id {
			var proof struct {
				ImageID string `json:"image_id"`
			}
			if u.a.request(ctx, "GET", "/self-update/"+c.ID+"/confirmation", nil, &proof) == nil && proof.ImageID == id {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}
