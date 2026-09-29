package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"github.com/scott4game/skygo/cluster"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type Docker struct{}

var variable = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func envName(s string) bool { return variable.MatchString(s) }

type boundedOutput struct {
	data  []byte
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := b.limit - len(b.data)
	if left > 0 {
		if left > n {
			left = n
		}
		b.data = append(b.data, p[:left]...)
	}
	return n, nil
}
func run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	out := &boundedOutput{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("docker operation failed")
	}
	return out.data, nil
}
func compose(s LocalService) []string {
	a := []string{"compose", "--project-name", s.Project, "--file", s.ComposeFile}
	if s.EnvFile != "" {
		a = append(a, "--env-file", s.EnvFile)
	}
	return a
}
func imageEnv(s LocalService, image string) []string {
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, s.ImageVariable+"=") {
			env = append(env, v)
		}
	}
	if image != "" {
		env = append(env, s.ImageVariable+"="+image)
	}
	return env
}
func (Docker) Observe(ctx context.Context, s LocalService) (control.Observation, error) {
	o := control.Observation{Service: s.ID}
	a := append(compose(s), "ps", "-a", "-q", s.ComposeService)
	b, err := run(ctx, os.Environ(), a...)
	if err != nil {
		return o, err
	}
	ids := strings.Fields(string(b))
	if len(ids) == 0 {
		return o, nil
	}
	if len(ids) != 1 {
		return o, errors.New("single container required")
	}
	raw, err := run(ctx, os.Environ(), "inspect", "--format", "{{json .}}", ids[0])
	if err != nil {
		return o, err
	}
	var state struct {
		Config struct{ Image string }
		State  struct {
			Running bool
			Health  *struct{ Status string }
		}
	}
	if json.Unmarshal(raw, &state) != nil {
		return o, errors.New("invalid container state")
	}
	o.Image = state.Config.Image
	o.Healthy = state.State.Running
	if state.State.Health != nil {
		o.Healthy = o.Healthy && state.State.Health.Status == "healthy"
	}
	if o.Healthy && s.HealthURL != "" {
		client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, "GET", s.HealthURL, nil)
		if err != nil {
			return o, errors.New("invalid local health URL")
		}
		resp, err := client.Do(req)
		if err != nil {
			o.Healthy = false
		} else {
			resp.Body.Close()
			o.Healthy = resp.StatusCode == 200
		}
	}
	if s.ConfigPath != "" {
		b, err := os.ReadFile(s.ConfigPath)
		if err == nil {
			o.ConfigHash = control.Digest(b)
		}
	}
	return o, nil
}
func (d Docker) Execute(ctx context.Context, s LocalService, c control.Command) control.Result {
	r := control.Result{ID: c.ID, Status: "failed", Code: "OPERATION_FAILED"}
	args := compose(s)
	env := os.Environ()
	switch c.Action {
	case "health":
	case "logs":
		if !s.AllowLogs {
			r.Code = "LOG_ACCESS_DISABLED"
			return r
		}
		raw, err := run(ctx, env, append(args, "logs", "--no-color", "--tail", "100", s.ComposeService)...)
		if err != nil {
			return r
		}
		r.Status = "succeeded"
		r.Code = "LOGS_COLLECTED"
		r.Logs = redact(string(raw))
		return r
	case "start":
		args = append(args, "start", s.ComposeService)
	case "stop":
		args = append(args, "stop", "--timeout", "30", s.ComposeService)
	case "restart":
		args = append(args, "restart", "--timeout", "30", s.ComposeService)
	case "deploy", "rollback":
		approved := false
		repository := strings.Split(c.Image, "@")[0]
		for _, prefix := range s.AllowedImages {
			if repository == prefix {
				approved = true
			}
		}
		if !approved {
			r.Code = "IMAGE_NOT_APPROVED"
			return r
		}
		env = imageEnv(s, c.Image)
		if _, err := run(ctx, env, "pull", c.Image); err != nil {
			r.Code = "IMAGE_PULL_FAILED"
			return r
		}
		args = append(args, "up", "-d", "--no-deps", "--pull", "never", s.ComposeService)
	case "configure":
		if s.ConfigPath == "" {
			r.Code = "CONFIG_NOT_APPROVED"
			return r
		}
		if s.SkygoRegistry {
			var snapshot cluster.Snapshot
			if json.Unmarshal(c.Config, &snapshot) != nil || snapshot.Validate() != nil {
				r.Code = "REGISTRY_INVALID"
				return r
			}
		}
		if settings.Atomic(s.ConfigPath, c.Config) != nil {
			r.Code = "CONFIG_WRITE_FAILED"
			return r
		}
		args = append(args, "restart", "--timeout", "30", s.ComposeService)
	default:
		return r
	}
	if c.Action != "health" {
		if _, err := run(ctx, env, args...); err != nil {
			r.Status = "uncertain"
			r.Code = "EXECUTION_NEEDS_RECONCILIATION"
			return r
		}
	}
	for {
		o, err := d.Observe(ctx, s)
		if err == nil {
			r.Healthy = o.Healthy
			r.Image = o.Image
			r.ConfigHash = o.ConfigHash
			if c.Action == "stop" { // Verify actual stopped state, not a failing health probe.
				b, e := run(ctx, env, append(compose(s), "ps", "--status", "running", "-q", s.ComposeService)...)
				if e == nil && len(strings.TrimSpace(string(b))) == 0 {
					r.Status = "succeeded"
					r.Code = "STOPPED"
					return r
				}
			} else if c.Action == "health" || (o.Healthy && ((c.Action != "deploy" && c.Action != "rollback") || o.Image == c.Image) && (c.Action != "configure" || o.ConfigHash == c.ConfigHash)) {
				r.Status = "succeeded"
				r.Code = "VERIFIED"
				return r
			}
		}
		select {
		case <-ctx.Done():
			r.Status = "uncertain"
			r.Code = "HEALTH_NOT_CONFIRMED"
			return r
		case <-time.After(time.Second):
		}
	}
}

var sensitiveLine = regexp.MustCompile(`(?i)(password|passwd|secret|token|authorization|cookie|private.key|dsn|credential|://[^ /]+:[^ /]+@)`)

func redact(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if sensitiveLine.MatchString(line) {
			lines[i] = "[redacted sensitive log line]"
		}
	}
	out := strings.Join(lines, "\n")
	if len(out) > 16384 {
		out = out[:16384]
	}
	return out
}
