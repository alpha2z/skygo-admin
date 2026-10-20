package baseline

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, Record, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	config := []byte(`{"os":"linux","architecture":"amd64"}`)
	imageID := "sha256:" + control.Digest(config)
	manifest, _ := json.Marshal([]map[string]any{{"Config": "config.json", "RepoTags": []string{"example/old:v1"}, "Layers": []string{}}})
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	for _, e := range []struct {
		name string
		raw  []byte
	}{{"config.json", config}, {"manifest.json", manifest}} {
		tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0600, Size: int64(len(e.raw))})
		tw.Write(e.raw)
	}
	tw.Close()
	r := Record{Version: 1, Host: "node", Service: "service", Tag: "example/old:v1", ImageID: imageID, ConfigID: imageID, Platform: "linux/amd64", ScopeRevision: strings.Repeat("a", 64), ArchiveSHA256: control.Digest(data.Bytes()), ArchiveSize: int64(data.Len()), Operator: "test-operator", Reason: "historical enrollment", CreatedAt: time.Now().UTC()}
	if err := os.WriteFile(filepath.Join(dir, r.ArchiveSHA256+".tar"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, r, pub, key
}
func TestSignedBaselineIdentityAndArchive(t *testing.T) {
	dir, r, pub, key := fixture(t)
	id, raw, err := Sign(r, key)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, id+".json"), raw, 0600)
	got, err := Read(dir, id, pub, true)
	if err != nil || got.ImageID != r.ImageID {
		t.Fatal(got, err)
	}
	id2, raw2, err := Sign(r, key)
	if err != nil || id != id2 || !bytes.Equal(raw, raw2) {
		t.Fatal("enrollment is not deterministic")
	}
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = Read(dir, id, wrong.Public().(ed25519.PublicKey), true); err == nil {
		t.Fatal("wrong signer accepted")
	}
	if _, err = Read(dir, "../"+id, pub, true); err == nil {
		t.Fatal("path traversal accepted")
	}
	f, _ := os.OpenFile(filepath.Join(dir, r.ArchiveSHA256+".tar"), os.O_WRONLY, 0600)
	f.WriteAt([]byte{1}, 1024)
	f.Close()
	if _, err = Read(dir, id, pub, true); err == nil {
		t.Fatal("corrupt archive accepted")
	}
}
func TestBaselineRejectsMismatchedContentAndMetadata(t *testing.T) {
	for _, field := range []string{"platform", "image", "tag", "size", "scope", "operator", "manifest-digest"} {
		t.Run(field, func(t *testing.T) {
			dir, r, _, key := fixture(t)
			path := filepath.Join(dir, r.ArchiveSHA256+".tar")
			switch field {
			case "platform":
				r.Platform = "linux/arm64"
			case "image":
				r.ImageID = "sha256:" + strings.Repeat("b", 64)
			case "tag":
				r.Tag = "example/other:v1"
			case "size":
				r.ArchiveSize++
			case "scope":
				r.ScopeRevision = ""
			case "operator":
				r.Operator = ""
			case "manifest-digest":
				r.Tag = "example/old@sha256:" + strings.Repeat("c", 64)
			}
			_, _, err := Sign(r, key)
			if err == nil {
				err = VerifyArchive(path, r)
			}
			if err == nil {
				t.Fatal("invalid baseline accepted")
			}
		})
	}
}
