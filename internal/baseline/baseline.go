// Package baseline verifies operator-attested historical rollback artifacts.
// These artifacts are never interpreted as registry releases.
package baseline

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/imagecontract"
	"github.com/alpha2z/skygo-admin/internal/transfer"
)

type Record struct {
	Version       int       `json:"version"`
	Host          string    `json:"host"`
	Service       string    `json:"service"`
	Tag           string    `json:"tag"`
	ConfigID      string    `json:"config_id"`
	ImageID       string    `json:"image_id"`
	Platform      string    `json:"platform"`
	ScopeRevision string    `json:"scope_revision"`
	ArchiveSHA256 string    `json:"archive_sha256"`
	ArchiveSize   int64     `json:"archive_size"`
	Operator      string    `json:"operator"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
}

var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var tag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*:[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

func ValidID(id string) bool { return digest.MatchString(id) }
func (r Record) Validate() error {
	if r.Version != 1 || !control.Identifier.MatchString(r.Host) || !control.Identifier.MatchString(r.Service) || !tag.MatchString(r.Tag) || strings.Contains(r.Tag, "@") || !control.ImageIdentity(r.ImageID) || !control.ImageIdentity(r.ConfigID) || !ValidID(r.ScopeRevision) || !ValidID(r.ArchiveSHA256) || r.ArchiveSize <= 0 || r.ArchiveSize > 8<<30 || (r.Platform != "linux/amd64" && r.Platform != "linux/arm64") || r.CreatedAt.IsZero() || strings.TrimSpace(r.Operator) == "" || len(r.Operator) > 128 || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 512 {
		return errors.New("invalid historical baseline")
	}
	return nil
}
func Read(dir, id string, key ed25519.PublicKey, archive bool) (Record, error) {
	var r Record
	if dir == "" || !ValidID(id) || len(key) != ed25519.PublicKeySize {
		return r, errors.New("historical baseline unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return r, err
	}
	var e control.Envelope
	if len(raw) > 16384 || json.Unmarshal(raw, &e) != nil || control.Digest(e.Payload) != id || !ed25519.Verify(key, append([]byte("skygo-admin/historical-baseline/v1\n"), e.Payload...), e.Signature) || json.Unmarshal(e.Payload, &r) != nil {
		return r, errors.New("historical baseline signature invalid")
	}
	if err = r.Validate(); err != nil {
		return r, err
	}
	if archive {
		err = VerifyArchive(filepath.Join(dir, r.ArchiveSHA256+".tar"), r)
	}
	return r, err
}
func Sign(r Record, key ed25519.PrivateKey) (string, []byte, error) {
	if err := r.Validate(); err != nil {
		return "", nil, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return "", nil, errors.New("invalid signing key")
	}
	raw, _ := json.Marshal(r)
	e := control.Envelope{Payload: raw, Signature: ed25519.Sign(key, append([]byte("skygo-admin/historical-baseline/v1\n"), raw...))}
	b, _ := json.Marshal(e)
	return control.Digest(raw), b, nil
}
func VerifyArchive(path string, r Record) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != r.ArchiveSize {
		return errors.New("historical archive size mismatch")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != r.ArchiveSHA256 {
		return errors.New("historical archive digest mismatch")
	}

	f.Seek(0, 0)
	entries := map[string]bool{}
	reader := tar.NewReader(f)
	for {
		header, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := filepath.Clean(header.Name)
		if entries[name] {
			return errors.New("duplicate historical archive member")
		}
		entries[name] = true
	}
	if err = transfer.ValidateArchiveImage(f, imagecontract.ReleaseImage{ImageID: r.ConfigID, Platform: r.Platform}); err != nil {
		return err
	}
	if r.ImageID != r.ConfigID {
		if _, err = transfer.ArchiveRuntimeID(f, r.ImageID, r.ConfigID, r.Platform); err != nil {
			return err
		}
	}
	f.Seek(0, 0)
	tr := tar.NewReader(f)
	for {
		head, e := tr.Next()
		if e != nil {
			return errors.New("historical archive tag missing")
		}
		if head.Name != "manifest.json" {
			continue
		}
		var m []struct{ RepoTags []string }
		if json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m) != nil || len(m) != 1 || len(m[0].RepoTags) != 1 || m[0].RepoTags[0] != r.Tag {
			return errors.New("historical archive tag mismatch")
		}
		return nil
	}
}

// ArchiveConfigID reads the exact configuration selected by a single-image export.
func ArchiveConfigID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var m []struct{ Config string }
	tr := tar.NewReader(f)
	for {
		h, e := tr.Next()
		if e != nil {
			return "", errors.New("archive manifest missing")
		}
		if h.Name == "manifest.json" {
			if h.Size > 1<<20 || json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m) != nil || len(m) != 1 {
				return "", errors.New("invalid archive manifest")
			}
			break
		}
	}
	f.Seek(0, 0)
	tr = tar.NewReader(f)
	for {
		h, e := tr.Next()
		if e != nil {
			return "", errors.New("archive config missing")
		}
		if h.Name == m[0].Config {
			if h.Size > 2<<20 {
				return "", errors.New("archive config too large")
			}
			raw, e := io.ReadAll(tr)
			if e != nil {
				return "", e
			}
			return "sha256:" + control.Digest(raw), nil
		}
	}
}
