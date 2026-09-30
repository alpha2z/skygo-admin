// Package agent executes signed operations against an operator-owned inventory.
package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type LocalService struct {
	Extensions     []string `json:"extensions,omitempty"`
	SyncImageEnv   bool     `json:"sync_image_env,omitempty"`
	CacheDir       string   `json:"-"`
	ID             string   `json:"id"`
	ComposeFile    string   `json:"compose_file"`
	EnvFile        string   `json:"env_file"`
	Project        string   `json:"project"`
	ComposeService string   `json:"compose_service"`
	ImageVariable  string   `json:"image_variable"`
	AllowedImages  []string `json:"allowed_images"`
	HealthURL      string   `json:"health_url"`
	ConfigPath     string   `json:"config_path"`
	SkygoRegistry  bool     `json:"skygo_registry"`
	AllowLogs      bool     `json:"allow_logs"`
}
type Config struct {
	Guard             func(context.Context, LocalService, control.Command) error `json:"-"`
	Extensions        map[string]ExtensionAction                                 `json:"-"`
	ControlUnit       *LocalUnit                                                 `json:"control_unit,omitempty"`
	HostID            string                                                     `json:"host_id"`
	APIURL            string                                                     `json:"api_url"`
	TokenFile         string                                                     `json:"token_file"`
	PublicKeyFile     string                                                     `json:"public_key_file"`
	StateDir          string                                                     `json:"state_dir"`
	AllowLoopbackHTTP bool                                                       `json:"allow_loopback_http"`
	Services          []LocalService                                             `json:"services"`
}
type Driver interface {
	Observe(context.Context, LocalService) (control.Observation, error)
	Execute(context.Context, LocalService, control.Command) control.Result
}
type receipt struct {
	Digest  string          `json:"digest"`
	Command control.Command `json:"command"`
	Result  control.Result  `json:"result"`
}
type Agent struct {
	boot   string
	cfg    Config
	key    ed25519.PublicKey
	token  string
	driver Driver
	client *http.Client
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	lock   *os.File
}

func New(c Config, d Driver) (*Agent, error) {
	u, err := url.Parse(c.APIURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "" {
		return nil, errors.New("invalid API origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !c.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTPS required")
		}
	}
	if !control.Identifier.MatchString(c.HostID) || !filepath.IsAbs(c.StateDir) {
		return nil, errors.New("invalid agent identity or state directory")
	}
	token, err := settings.Secret(c.TokenFile)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(c.PublicKeyFile)
	if err != nil {
		return nil, errors.New("public key unavailable")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key")
	}
	for i := range c.Services {
		c.Services[i].CacheDir = filepath.Join(c.StateDir, "images")
	}
	for name, x := range c.Extensions {
		if !control.Identifier.MatchString(name) || x.Execute == nil || x.Reconcile == nil || x.Validate == nil {
			return nil, errors.New("invalid extension registration")
		}
	}
	seen := map[string]bool{}
	for _, s := range c.Services {
		if !control.Identifier.MatchString(s.ID) || seen[s.ID] || !filepath.IsAbs(s.ComposeFile) || !control.Identifier.MatchString(s.Project) || !control.Identifier.MatchString(s.ComposeService) || !envName(s.ImageVariable) {
			return nil, errors.New("invalid local service")
		}
		if (s.EnvFile != "" && !filepath.IsAbs(s.EnvFile)) || (s.SyncImageEnv && s.EnvFile == "") {
			return nil, errors.New("absolute env file required")
		}
		if s.ConfigPath != "" && !filepath.IsAbs(s.ConfigPath) {
			return nil, errors.New("absolute config path required")
		}
		for _, prefix := range s.AllowedImages {
			if prefix == "" || strings.ContainsAny(prefix, " \t\r\n@") {
				return nil, errors.New("invalid image allowlist")
			}
		}
		for _, name := range s.Extensions {
			if _, ok := c.Extensions[name]; !ok {
				return nil, errors.New("unregistered local extension")
			}
		}
		seen[s.ID] = true
	}
	if err := validateLocalUnit(c); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(c.StateDir, 0700); err != nil {
		return nil, err
	}
	return &Agent{boot: "b" + control.Digest([]byte(time.Now().String()))[:32], cfg: c, key: key, token: token, driver: d, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *Agent) Start(parent context.Context) error {
	f, err := os.OpenFile(filepath.Join(a.cfg.StateDir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return errors.New("agent already running")
	}
	a.lock = f
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	a.done = make(chan struct{})
	go func() { defer close(a.done); a.run(ctx) }()
	return nil
}
func (a *Agent) Stop(ctx context.Context) error {
	if a.cancel == nil {
		return nil
	}
	a.cancel()
	select {
	case <-a.done:
		syscall.Flock(int(a.lock.Fd()), syscall.LOCK_UN)
		return a.lock.Close()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *Agent) request(ctx context.Context, method, path string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.APIURL, "/")+"/agent/v1"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Host-ID", a.cfg.HostID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("controller unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("controller rejected request")
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 12<<20)).Decode(out)
	}
	return nil
}
func (a *Agent) run(ctx context.Context) {
	boot := a.boot
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for {
			observations := []control.Observation{}
			for _, s := range a.cfg.Services {
				c, cancel := context.WithTimeout(ctx, 5*time.Second)
				o, err := a.driver.Observe(c, s)
				cancel()
				if err == nil {
					if _, ok := a.driver.(Docker); ok {
						o.Capabilities = append(o.Capabilities, "image.archive.v1", "image.cleanup.v1")
					}
					o.SyncImageEnv = s.SyncImageEnv
					if s.SyncImageEnv {
						o.Capabilities = append(o.Capabilities, "image.env.v1")
						o.InventoryRevision, _ = a.unitRevision(s)
					}
					for _, name := range s.Extensions {
						o.Capabilities = append(o.Capabilities, "extension."+name+".v1")
						if x, ok := a.cfg.Extensions[name]; ok && x.Observe != nil {
							work, done := context.WithTimeout(ctx, 5*time.Second)
							data, err := x.Observe(work, s)
							done()
							if err == nil && len(data) <= 16<<10 && json.Valid(data) {
								if o.Extensions == nil {
									o.Extensions = map[string]json.RawMessage{}
								}
								o.Extensions[name] = data
							}
						}
					}
					a.decorateUnit(s, &o)
					observations = append(observations, o)
				}
			}
			a.request(ctx, "POST", "/heartbeat", control.Heartbeat{Version: 1, BootID: boot, Observations: observations}, nil)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	defer func() { <-heartbeatDone }()
	a.recoverUnits(ctx)
	a.recoverEnvDeployments(ctx)
	for {
		var commands []control.Envelope
		if a.request(ctx, "GET", "/commands", nil, &commands) == nil {
			for _, envelope := range commands {
				if ctx.Err() != nil {
					return
				}
				result := a.Handle(ctx, envelope)
				if result.ID != "" {
					a.request(ctx, "POST", "/results", result, nil)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// Handle journals intent before executing. The same signed command can only
// execute a service mutation once. A crash is reconciled from observable state
// or left uncertain; interrupted image-cache preparation is safely repeatable.
func (a *Agent) Handle(ctx context.Context, e control.Envelope) control.Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	cmd, err := control.Verify(e, a.key, a.cfg.HostID)
	if err != nil {
		return control.Result{Status: "failed", Code: "COMMAND_REJECTED"}
	}
	if cmd.Action == "control-unit" {
		return a.handleUnit(ctx, e, cmd)
	}
	result := control.Result{ID: cmd.ID, Status: "failed", Code: "COMMAND_REJECTED"}
	var service LocalService
	found := false
	for _, s := range a.cfg.Services {
		if s.ID == cmd.Service {
			service = s
			found = true
		}
	}
	if !found {
		return result
	}

	if (cmd.Action == "deploy" || cmd.Action == "rollback") && (service.SyncImageEnv || cmd.SyncImageEnv) {
		return a.handleEnvDeployment(ctx, e, cmd, service)
	}
	path := filepath.Join(a.cfg.StateDir, cmd.ID+".json")
	hash := control.Digest(e.Payload)
	b, err := os.ReadFile(path)
	if err == nil {
		var old receipt
		if json.Unmarshal(b, &old) != nil || old.Digest != hash {
			result.Code = "RECEIPT_CONFLICT"
			return result
		}
		if old.Result.Status != "uncertain" {
			return old.Result
		}
		if cmd.Action == "prepare-image" && time.Now().Before(cmd.ExpiresAt) { // Cache population is repeatable; never repeat a service mutation.
			work, cancel := context.WithTimeout(ctx, 30*time.Minute)
			result = a.execute(work, service, cmd)
			cancel()
			result.ID = cmd.ID
			old.Result = result
			if a.save(path, old) != nil {
				return control.Result{ID: cmd.ID, Status: "uncertain", Code: "JOURNAL_UNAVAILABLE"}
			}
			return result
		}
		result = a.reconcile(ctx, service, cmd)
		if result.Status == "succeeded" {
			old.Result = result
			if a.save(path, old) != nil {
				return control.Result{ID: cmd.ID, Status: "uncertain", Code: "JOURNAL_UNAVAILABLE"}
			}
		}
		return result
	}
	if !os.IsNotExist(err) {
		result.Code = "JOURNAL_UNAVAILABLE"
		return result
	}
	if !time.Now().Before(cmd.ExpiresAt) {
		result.Code = "COMMAND_EXPIRED"
		return result
	}
	if cmd.Action == "extension" {
		x, ok := a.extension(service, cmd.Extension)
		if !ok || x.Validate(cmd.Payload) != nil {
			return result
		}
	}
	if a.cfg.Guard != nil {
		if err := a.cfg.Guard(ctx, service, cmd); err != nil {
			result.Code = "LOCAL_POLICY_REJECTED"
			if a.save(path, receipt{Digest: hash, Command: cmd, Result: result}) != nil {
				return control.Result{ID: cmd.ID, Status: "uncertain", Code: "JOURNAL_UNAVAILABLE"}
			}
			return result
		}
	}
	rec := receipt{Digest: hash, Command: cmd, Result: control.Result{ID: cmd.ID, Status: "uncertain", Code: "EXECUTION_INTERRUPTED"}}
	if a.save(path, rec) != nil {
		result.Code = "JOURNAL_UNAVAILABLE"
		return result
	}
	duration := 2 * time.Minute
	if cmd.Action == "prepare-image" {
		duration = 30 * time.Minute
	}
	execution, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	result = a.execute(execution, service, cmd)
	result.ID = cmd.ID
	rec.Result = result
	if a.save(path, rec) != nil {
		return control.Result{ID: cmd.ID, Status: "uncertain", Code: "JOURNAL_UNAVAILABLE"}
	}
	return result
}
func (a *Agent) save(path string, r receipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return settings.Atomic(path, b)
}
func (a *Agent) reconcile(ctx context.Context, s LocalService, c control.Command) control.Result {
	if c.Action == "extension" {
		if x, ok := a.extension(s, c.Extension); ok {
			return x.Reconcile(ctx, s, c)
		}
		return control.Result{ID: c.ID, Status: "uncertain", Code: "EXTENSION_UNAVAILABLE"}
	}
	r := control.Result{ID: c.ID, Status: "uncertain", Code: "MANUAL_RECONCILIATION_REQUIRED"}
	o, err := a.driver.Observe(ctx, s)
	if err != nil {
		return r
	}
	ok := false
	switch c.Action {
	case "deploy", "rollback":
		ok = o.Healthy && o.Image == c.Image
	case "start":
		ok = o.Healthy
	case "configure":
		ok = o.Healthy && o.ConfigHash == c.ConfigHash
	case "health":
		ok = true
	}
	if ok {
		r.Status = "succeeded"
		r.Code = "RECONCILED"
		r.Healthy = o.Healthy
		r.Image = o.Image
		r.ConfigHash = o.ConfigHash
	}
	return r
}
