// Package githubbuild controls only one locally approved GitHub workflow.
// It never returns credentials, raw GitHub errors, logs or artifact URLs.
package githubbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	AutoRegister  bool     `json:"auto_register,omitempty"`
	PublishImages bool     `json:"publish_images,omitempty"`
	Repository    string   `json:"repository"`
	Workflow      string   `json:"workflow"`
	AllowedRefs   []string `json:"allowed_refs"`
	TokenFile     string   `json:"token_file,omitempty"`
}
type Client struct {
	messageMu sync.Mutex
	messages  map[string]commitMessageEntry
	config    Config
	http      *http.Client
	origin    string
}
type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string { return e.Code }

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var workflowPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*\.ya?ml$`)
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,199}$`)
var Services = []string{"admin-api", "admin-web", "ops-agent", "all"}

func New(config Config) (*Client, error) {
	if !repositoryPattern.MatchString(config.Repository) || !workflowPattern.MatchString(config.Workflow) || !filepath.IsAbs(config.TokenFile) || len(config.AllowedRefs) == 0 || len(config.AllowedRefs) > 20 {
		return nil, errors.New("invalid approved GitHub configuration")
	}
	for _, ref := range config.AllowedRefs {
		if !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") {
			return nil, errors.New("invalid approved GitHub ref")
		}
	}
	return &Client{config: config, origin: "https://api.github.com", http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Config() Config {
	out := c.config
	out.TokenFile = ""
	out.AllowedRefs = append([]string(nil), c.config.AllowedRefs...)
	return out
}
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	file, err := os.OpenFile(c.config.TokenFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return &Error{Code: "GITHUB_CREDENTIAL_UNAVAILABLE", Status: 503}
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return &Error{Code: "GITHUB_CREDENTIAL_UNAVAILABLE", Status: 503}
	}
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	file.Close()
	token := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 8192 || token == "" || strings.ContainsAny(token, " \t\r\n\x00") {
		return &Error{Code: "GITHUB_CREDENTIAL_UNAVAILABLE", Status: 503}
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
	}
	if err != nil {
		return &Error{Code: "GITHUB_INVALID_REQUEST", Status: 400}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(data))
	if err != nil {
		return &Error{Code: "GITHUB_INVALID_REQUEST", Status: 400}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "skygo-admin")
	response, err := c.http.Do(req)
	if err != nil {
		return &Error{Code: "GITHUB_CONNECTION_FAILED", Status: 503}
	}
	defer response.Body.Close()
	if target, ok := out.(*artifactRedirect); ok && response.StatusCode == http.StatusFound {
		target.location = response.Header.Get("Location")
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "GITHUB_REQUEST_REJECTED"
		if response.StatusCode >= 500 {
			code = "GITHUB_UNAVAILABLE"
		}
		switch response.StatusCode {
		case 401:
			code = "GITHUB_AUTH_FAILED"
		case 403:
			code = "GITHUB_ACCESS_DENIED"
		case 404:
			code = "GITHUB_WORKFLOW_UNAVAILABLE"
		case 429:
			code = "GITHUB_RATE_LIMITED"
		case 422:
			code = "GITHUB_INPUT_REJECTED"
		}
		return &Error{Code: code, Status: 502}
	}
	if out == nil {
		return nil
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(content) > 2<<20 {
		return &Error{Code: "GITHUB_RESPONSE_INVALID", Status: 502}
	}
	if json.Unmarshal(content, out) != nil {
		return &Error{Code: "GITHUB_RESPONSE_INVALID", Status: 502}
	}
	return nil
}
func (c *Client) path(suffix string) string {
	return "/repos/" + c.config.Repository + "/actions/workflows/" + c.config.Workflow + suffix
}

type Run struct {
	StartedAt     *time.Time `json:"run_started_at,omitempty"`
	CommitMessage string     `json:"commit_message"`
	HeadCommit    *struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit,omitempty"`
	Attempt    int       `json:"run_attempt"`
	ID         int64     `json:"id"`
	Number     int64     `json:"run_number"`
	Status     string    `json:"status"`
	Conclusion *string   `json:"conclusion"`
	Commit     string    `json:"head_sha"`
	Ref        string    `json:"head_branch"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
	HTMLURL    string    `json:"html_url"`
}

func (c *Client) Runs(ctx context.Context) ([]Run, error) { return c.runs(ctx, 1) }

// SyncRuns bounds discovery to the latest 150 runs.
func (c *Client) SyncRuns(ctx context.Context) ([]Run, error) { return c.runs(ctx, 5) }
func (c *Client) runs(ctx context.Context, pages int) ([]Run, error) {
	messageContext, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	runs := []Run{}
	for page := 1; page <= pages; page++ {
		var response struct {
			Runs []Run `json:"workflow_runs"`
		}
		if err := c.request(ctx, "GET", c.path("/runs?per_page=30"+func() string {
			if page == 1 {
				return ""
			}
			return "&page=" + jsonNumber(int64(page))
		}()), nil, &response); err != nil {
			return nil, err
		}
		for _, run := range response.Runs {
			allowed := false
			for _, ref := range c.config.AllowedRefs {
				if run.Ref == ref {
					allowed = true
				}
			}
			if !allowed || run.ID <= 0 {
				continue
			}
			// Build links locally rather than trusting API-provided arbitrary URLs.
			run.HTMLURL = "https://github.com/" + c.config.Repository + "/actions/runs/" + url.PathEscape(jsonNumber(run.ID))
			if pages == 1 {
				run.CommitMessage = c.commitMessage(messageContext, run)
			}
			run.HeadCommit = nil
			runs = append(runs, run)
		}
		if len(response.Runs) < 30 {
			break
		}
	}
	return runs, nil
}
func jsonNumber(value int64) string { raw, _ := json.Marshal(value); return string(raw) }
func (c *Client) ValidateDispatch(ref, service, platform string) error {
	allowed := false
	for _, entry := range c.config.AllowedRefs {
		if ref == entry {
			allowed = true
		}
	}
	selected := false
	for _, entry := range Services {
		if service == entry {
			selected = true
		}
	}
	if !allowed || !selected || (platform != "linux/amd64" && platform != "linux/arm64") {
		return &Error{Code: "GITHUB_INPUT_NOT_APPROVED", Status: 400}
	}
	return nil
}

// Dispatch is deliberately not retried: after a connection failure GitHub may
// already have accepted the build. The caller must retain an uncertain record.
func (c *Client) Dispatch(ctx context.Context, ref, service, platform string) error {
	if err := c.ValidateDispatch(ref, service, platform); err != nil {
		return err
	}
	inputs := map[string]string{"service": service, "platform": platform}
	if c.config.PublishImages {
		inputs["publish"] = "true"
	}
	return c.request(ctx, "POST", c.path("/dispatches"), map[string]any{"ref": ref, "inputs": inputs}, nil)
}

type commitMessageEntry struct {
	Message string
	Expires time.Time
}

func (c *Client) commitMessage(ctx context.Context, r Run) string {
	if !commitPattern.MatchString(r.Commit) {
		return ""
	}
	trim := func(s string) string {
		if len(s) > 2048 {
			s = s[:2048]
		}
		return s
	}
	if r.HeadCommit != nil && r.HeadCommit.ID == r.Commit {
		return trim(r.HeadCommit.Message)
	}
	c.messageMu.Lock()
	entry, ok := c.messages[r.Commit]
	c.messageMu.Unlock()
	if ok && time.Now().Before(entry.Expires) {
		return entry.Message
	}
	if ctx.Err() != nil {
		return ""
	}
	var response struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if c.request(ctx, "GET", "/repos/"+c.config.Repository+"/commits/"+r.Commit, nil, &response) != nil || response.SHA != r.Commit {
		return ""
	}
	message := trim(response.Commit.Message)
	c.messageMu.Lock()
	defer c.messageMu.Unlock()
	if c.messages == nil || len(c.messages) >= 256 {
		c.messages = map[string]commitMessageEntry{}
	}
	c.messages[r.Commit] = commitMessageEntry{Message: message, Expires: time.Now().Add(time.Hour)}
	return message
}
