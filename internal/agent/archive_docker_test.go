package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveAgentDocker(t *testing.T) {
	if os.Getenv("SKYGO_ADMIN_ARCHIVE_DOCKER_TEST") != "1" {
		t.Skip("run scripts/archive-smoke.py with isolated Docker fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var layer bytes.Buffer
	lw := tar.NewWriter(&layer)
	content := []byte("synthetic archive fixture " + t.TempDir())
	lw.WriteHeader(&tar.Header{Name: "fixture.txt", Mode: 0600, Size: int64(len(content))})
	lw.Write(content)
	lw.Close()
	layerID := "sha256:" + control.Digest(layer.Bytes())
	config, _ := json.Marshal(map[string]any{"os": "linux", "architecture": "arm64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{layerID}}})
	imageID := "sha256:" + control.Digest(config)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": map[string]any{"digest": imageID, "size": len(config), "mediaType": "application/vnd.oci.image.config.v1+json"}, "layers": []any{map[string]any{"digest": layerID, "size": layer.Len(), "mediaType": "application/vnd.oci.image.layer.v1.tar"}}})
	rootID := "sha256:" + control.Digest(manifest)
	reference := "ghcr.io/example/synthetic@" + rootID
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": rootID, "size": len(manifest), "platform": map[string]string{"os": "linux", "architecture": "arm64"}}}})
	legacy, _ := json.Marshal([]any{map[string]any{"Config": "blobs/sha256/" + imageID[7:], "RepoTags": []string{}, "Layers": []string{"layer.tar"}}})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, entry := range []struct {
		name string
		raw  []byte
	}{{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)}, {"index.json", index}, {"blobs/sha256/" + rootID[7:], manifest}, {"blobs/sha256/" + imageID[7:], config}, {"blobs/sha256/" + layerID[7:], layer.Bytes()}, {"layer.tar", layer.Bytes()}, {"manifest.json", legacy}} {
		tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.raw))})
		tw.Write(entry.raw)
	}
	tw.Close()
	archiveID := control.Digest(archive.Bytes())
	var downloads atomic.Int64
	var resumed atomic.Bool
	token := strings.Repeat("t", 40)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Host-ID") != "host" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/attempts"):
			w.Write([]byte(`{"attempt":1}`))
		case strings.HasSuffix(r.URL.Path, "/progress"):
			w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/archive"):
			downloads.Add(1)
			if r.Header.Get("Range") == "bytes=512-" {
				resumed.Store(true)
			}
			http.ServeContent(w, r, "archive", time.Time{}, bytes.NewReader(archive.Bytes()))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	dir := t.TempDir()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	settings.Atomic(filepath.Join(dir, "token"), []byte(token))
	settings.Atomic(filepath.Join(dir, "public"), []byte(base64.StdEncoding.EncodeToString(pub)))
	os.MkdirAll(filepath.Join(dir, "state", "images"), 0700)
	os.WriteFile(filepath.Join(dir, "state", "images", archiveID+".partial"), archive.Bytes()[:512], 0600)
	a, err := New(Config{HostID: "host", APIURL: api.URL, AllowLoopbackHTTP: true, TokenFile: filepath.Join(dir, "token"), PublicKeyFile: filepath.Join(dir, "public"), StateDir: filepath.Join(dir, "state"), Services: []LocalService{{ID: "api", ComposeFile: "/nonexistent/compose.yaml", ComposeService: "api", Project: "synthetic", ImageVariable: "API_IMAGE", AllowedImages: []string{"ghcr.io/example/synthetic"}}}}, Docker{})
	if err != nil {
		t.Fatal(err)
	}
	c := control.Command{Version: 1, ID: "archive", DeliveryID: "delivery", HostID: "host", Service: "api", Action: "prepare-image", Image: reference, Platform: "linux/arm64", Archive: &control.Archive{SHA256: archiveID, Size: int64(archive.Len()), ImageID: imageID}, ExpiresAt: time.Now().Add(time.Minute)}
	e, err := control.Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	result := a.Handle(ctx, e)
	if result.Status != "succeeded" {
		t.Fatal("archive prepare failed", result.Code)
	}
	defer run(context.Background(), os.Environ(), "image", "rm", "--no-prune", result.ImageID)
	if !resumed.Load() || downloads.Load() != 1 {
		t.Fatal("partial archive was not resumed")
	}
	if again := a.Handle(ctx, e); again.Status != "succeeded" || downloads.Load() != 1 {
		t.Fatal("replay reimported archive")
	}
	s := a.cfg.Services[0]
	if err := (Docker{}).UnitImage(ctx, s, reference, result.ImageID, c.Platform); err != nil {
		t.Fatal("archive evidence unavailable to deployment")
	}
	if _, err := os.Stat(s.ComposeFile); !os.IsNotExist(err) {
		t.Fatal("prepare unexpectedly created compose")
	}
	cleanup := control.Command{Version: 1, ID: "cleanup-image", HostID: "host", Service: "api", Action: "image-cleanup", Image: reference, Cleanup: &control.Cleanup{Kind: "image", ImageID: result.ImageID, Platform: c.Platform}, ExpiresAt: time.Now().Add(time.Minute)}
	signed, _ := control.Sign(cleanup, key)
	if deleted := a.Handle(ctx, signed); deleted.Status != "succeeded" {
		t.Fatal("safe image cleanup failed", deleted.Code)
	}
}
