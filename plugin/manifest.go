package plugin

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is immutable package metadata; installation policy is kept separately.
type Manifest struct {
	Protocol     int               `json:"protocol"`
	ID           string            `json:"id"`
	Version      string            `json:"version"`
	CoreProtocol int               `json:"core_protocol"`
	Capabilities []string          `json:"capabilities"`
	Assets       map[string]string `json:"assets"`
}
type SignedManifest struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
}
type Installation struct {
	Endpoint            Endpoint `json:"endpoint"`
	ManifestFile        string   `json:"manifest_file"`
	AssetsDirectory     string   `json:"assets_directory"`
	AllowedCapabilities []string `json:"allowed_capabilities"`
}

func VerifyManifest(raw []byte, key ed25519.PublicKey) (Manifest, string, error) {
	var envelope SignedManifest
	var m Manifest
	if len(raw) > MaxBody || len(key) != ed25519.PublicKeySize || json.Unmarshal(raw, &envelope) != nil || !ed25519.Verify(key, envelope.Payload, envelope.Signature) {
		return m, "", errors.New("untrusted plugin manifest")
	}
	if json.Unmarshal(envelope.Payload, &m) != nil || m.Protocol != Version || m.CoreProtocol != Version || !identifier.MatchString(m.ID) || m.Version == "" || len(m.Version) > 64 {
		return m, "", errors.New("incompatible plugin manifest")
	}
	seen := map[string]bool{}
	for _, c := range m.Capabilities {
		if !identifier.MatchString(c) || seen[c] {
			return m, "", errors.New("invalid plugin capability")
		}
		seen[c] = true
	}
	for name, digest := range m.Assets {
		if !fs.ValidPath(name) || strings.Contains(name, "\\") || !revision.MatchString(digest) {
			return m, "", errors.New("invalid plugin asset")
		}
	}
	hash := sha256.Sum256(envelope.Payload)
	return m, hex.EncodeToString(hash[:]), nil
}

// GrantedCapabilities computes an intersection; it never silently grants new capabilities.
func GrantedCapabilities(m Manifest, allowed []string) ([]string, error) {
	declared := map[string]bool{}
	for _, v := range m.Capabilities {
		declared[v] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, v := range allowed {
		if !declared[v] || seen[v] {
			return nil, errors.New("installation requests undeclared capability")
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

// ReadAssets validates a fixed asset set into memory, avoiding later path/symlink changes.
func ReadAssets(directory string, m Manifest) (map[string][]byte, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("plugin asset directory must be absolute")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("unsafe plugin asset directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	out := map[string][]byte{}
	total := 0
	for name, want := range m.Assets {
		parts := strings.Split(name, "/")
		prefix := ""
		for _, part := range parts {
			if prefix != "" {
				prefix += "/"
			}
			prefix += part
			info, err := root.Lstat(prefix)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
				return nil, errors.New("unsafe plugin asset path")
			}
		}
		info, statErr := root.Stat(name)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil, errors.New("invalid plugin asset size")
		}
		b, err := root.ReadFile(name)
		total += len(b)
		if err != nil || len(b) > 4<<20 || total > 16<<20 {
			return nil, errors.New("plugin assets exceed limits")
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != want {
			return nil, errors.New("plugin asset digest mismatch")
		}
		out[name] = b
	}
	return out, nil
}

// Trust binds an installation to a separately provisioned Ed25519 trust key.
type Trust struct {
	ManifestFile  string `json:"manifest_file"`
	PublicKeyFile string `json:"public_key_file"`
}

func (t Trust) Verify(endpoint Endpoint, allowed []string) error {
	if !filepath.IsAbs(t.ManifestFile) || !filepath.IsAbs(t.PublicKeyFile) {
		return errors.New("plugin trust files must be absolute")
	}
	var signed SignedManifest
	if err := ReadConfig(t.ManifestFile, &signed); err != nil {
		return err
	}
	// Re-marshalling RawMessage preserves its compact representation only. Read the
	// original file again to verify the exact signed bytes, including whitespace.
	raw, err := os.ReadFile(t.ManifestFile)
	if err != nil {
		return err
	}
	k, err := os.ReadFile(t.PublicKeyFile)
	if err != nil {
		return errors.New("plugin trust key unavailable")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(k)))
	if err != nil {
		return errors.New("invalid plugin trust key")
	}
	m, rev, err := VerifyManifest(raw, key)
	if err != nil {
		return err
	}
	if endpoint.ID != m.ID || endpoint.Revision != rev {
		return errors.New("installed plugin identity mismatch")
	}
	_, err = GrantedCapabilities(m, allowed)
	return err
}
