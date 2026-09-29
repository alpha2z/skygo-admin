// Package release describes signed build provenance, not deployment authority.
package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/control"
	"io"
	"regexp"
)

type Build struct {
	Repository   string `json:"repository"`
	Workflow     string `json:"workflow"`
	Ref          string `json:"ref"`
	SourceCommit string `json:"source_commit"`
	RunID        int64  `json:"run_id"`
	RunAttempt   int    `json:"run_attempt"`
}
type Image struct {
	Service   string `json:"service"`
	Platform  string `json:"platform"`
	Reference string `json:"reference"`
}
type Manifest struct {
	Version int     `json:"version"`
	ID      string  `json:"id"`
	Build   Build   `json:"build"`
	Images  []Image `json:"images"`
}
type SignedManifest struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
}

func (m Manifest) Validate() error {
	b := m.Build
	if m.Version != 1 || m.ID != fmt.Sprintf("ci-%d-%d", b.RunID, b.RunAttempt) || b.RunID <= 0 || b.RunAttempt < 1 || b.RunAttempt > 10000 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(b.Repository) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*\.ya?ml$`).MatchString(b.Workflow) || !regexp.MustCompile(`^refs/heads/[A-Za-z0-9][A-Za-z0-9_./-]{0,199}$`).MatchString(b.Ref) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(b.SourceCommit) || len(m.Images) < 1 || len(m.Images) > 6 {
		return errors.New("invalid build provenance")
	}
	seen := map[string]bool{}
	for _, i := range m.Images {
		if (i.Service != "admin-api" && i.Service != "admin-web" && i.Service != "ops-agent") || !Platform(i.Platform) || !control.Image.MatchString(i.Reference) || seen[i.Service+"/"+i.Platform] {
			return errors.New("invalid release image")
		}
		seen[i.Service+"/"+i.Platform] = true
	}
	return nil
}
func Platform(s string) bool { return s == "linux/amd64" || s == "linux/arm64" }
func Decode(s SignedManifest) (Manifest, error) {
	var m Manifest
	if len(s.Payload) > 64<<10 || len(s.Signature) != ed25519.SignatureSize {
		return m, errors.New("invalid release envelope")
	}
	d := json.NewDecoder(bytes.NewReader(s.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil {
		return m, errors.New("invalid release payload")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return m, errors.New("trailing release data")
	}
	return m, m.Validate()
}
func Sign(m Manifest, key ed25519.PrivateKey) (SignedManifest, error) {
	if err := m.Validate(); err != nil {
		return SignedManifest{}, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return SignedManifest{}, errors.New("invalid build key")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return SignedManifest{}, err
	}
	return SignedManifest{b, ed25519.Sign(key, b)}, nil
}
func Verify(s SignedManifest, keys []ed25519.PublicKey) (Manifest, error) {
	m, err := Decode(s)
	if err != nil {
		return m, err
	}
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, s.Payload, s.Signature) {
			return m, nil
		}
	}
	return Manifest{}, errors.New("untrusted build signature")
}
