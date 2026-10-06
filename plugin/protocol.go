// Package plugin defines the local, versioned protocol for operator-installed plugins.
// Plugins are trusted sidecars, not downloadable or sandboxed code.
package plugin

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
	"regexp"
	"time"
)

const Version = 1
const MaxBody = 1 << 20

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var revision = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Endpoint struct {
	ID       string `json:"id"`
	Socket   string `json:"socket"`
	Revision string `json:"revision"`
}
type Request struct {
	Observation json.RawMessage  `json:"observation,omitempty"`
	Resource    *ResourceRequest `json:"resource,omitempty"`
	Version     int              `json:"version"`
	Plugin      string           `json:"plugin"`
	Revision    string           `json:"revision"`
	Operation   string           `json:"operation"`
	Action      string           `json:"action,omitempty"`
	Service     string           `json:"service,omitempty"`
	Payload     json.RawMessage  `json:"payload,omitempty"`
	Command     json.RawMessage  `json:"command,omitempty"`
}
type Response struct {
	Version  int             `json:"version"`
	Revision string          `json:"revision"`
	Data     json.RawMessage `json:"data,omitempty"`
}

func (e Endpoint) Validate() error {
	if !identifier.MatchString(e.ID) || !filepath.IsAbs(e.Socket) || filepath.Clean(e.Socket) != e.Socket || !revision.MatchString(e.Revision) {
		return errors.New("invalid local plugin endpoint")
	}
	return nil
}

// Call never retries or follows redirects. Uncertain execution must be reconciled.
func (e Endpoint) Call(ctx context.Context, r Request, out any) error {
	if err := e.Validate(); err != nil {
		return err
	}
	r.Version = Version
	r.Plugin = e.ID
	r.Revision = e.Revision
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxBody {
		return errors.New("invalid plugin request")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", e.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://plugin/v1/call", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("local plugin unavailable")
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, MaxBody+1))
	if err != nil || len(b) > MaxBody || response.StatusCode != 200 {
		return errors.New("local plugin rejected operation")
	}
	var result Response
	if json.Unmarshal(b, &result) != nil || result.Version != Version || result.Revision != e.Revision {
		return errors.New("plugin protocol or revision mismatch")
	}
	if out != nil && json.Unmarshal(result.Data, out) != nil {
		return errors.New("invalid plugin response")
	}
	return nil
}

// ReadConfig requires an operator-owned regular file, with no symlink or group/world writes.
func ReadConfig(name string, out any) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe plugin configuration")
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, MaxBody+1))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid plugin configuration")
	}
	return nil
}
