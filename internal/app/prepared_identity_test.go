package app

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func identityDigest(b []byte) string {
	v := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(v[:])
}
func identityArchive(t *testing.T) (control.Command, []byte, string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	cid := identityDigest(config)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]string{"digest": cid}})
	mid := identityDigest(manifest)
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{"digest": mid, "platform": map[string]string{"os": "linux", "architecture": "amd64"}}}})
	rid := identityDigest(index)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, b := range [][]byte{config, manifest, index} {
		if err := w.WriteHeader(&tar.Header{Name: "blobs/sha256/" + identityDigest(b)[7:], Mode: 0600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	return control.Command{Service: "agent", Image: "example.invalid/agent@" + rid, Platform: "linux/amd64", Archive: &control.Archive{SHA256: identityDigest(raw)[7:], Size: int64(len(raw)), ImageID: cid}}, raw, mid
}
func TestPreparedIdentityBoundToSignedArchive(t *testing.T) {
	c, raw, manifest := identityArchive(t)
	dir := t.TempDir()
	s := &Server{distribution: &distributionConfig{Directory: dir}}
	path := filepath.Join(dir, c.Archive.SHA256)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if !s.preparedImageIdentity(c, c.Archive.ImageID) || !s.preparedImageIdentity(c, manifest) {
		t.Fatal("valid classic/containerd identity rejected")
	}
	if s.preparedImageIdentity(c, "sha256:"+strings.Repeat("f", 64)) {
		t.Fatal("unrelated identity accepted")
	}
	wrong := c
	wrong.Platform = "linux/arm64"
	if s.preparedImageIdentity(wrong, manifest) {
		t.Fatal("wrong platform accepted")
	}
	wrong = c
	wrong.Image = "example.invalid/agent@sha256:" + strings.Repeat("e", 64)
	if s.preparedImageIdentity(wrong, manifest) {
		t.Fatal("unlinked registry root accepted")
	}
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if s.preparedImageIdentity(c, manifest) {
		t.Fatal("modified archive accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s.preparedImageIdentity(c, manifest) {
		t.Fatal("missing archive accepted")
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if s.preparedImageIdentity(c, manifest) {
		t.Fatal("symlink cache accepted")
	}
}
