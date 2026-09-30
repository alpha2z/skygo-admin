package registrycache

import (
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheCleanupPreservesSharedAndUnknownLayers(t *testing.T) {
	root := t.TempDir()
	blob := digest([]byte("shared"))
	os.MkdirAll(filepath.Join(root, "registry-blobs"), 0700)
	path := filepath.Join(root, "registry-blobs", blob[7:])
	os.WriteFile(path, []byte("shared"), 0600)
	foreign := filepath.Join(root, "registry-blobs", strings.Repeat("f", 64))
	os.WriteFile(foreign, []byte("not managed by these images"), 0600)
	images := []ops.ReleaseImage{{Role: "admin-api", Platform: "linux/amd64", Reference: "ghcr.io/owner/admin-api@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64)}, {Role: "admin-web", Platform: "linux/amd64", Reference: "ghcr.io/owner/admin-web@sha256:" + strings.Repeat("c", 64), ImageID: "sha256:" + strings.Repeat("d", 64)}}
	for _, image := range images {
		work := filepath.Join(root, ".registry-"+image.Key())
		os.MkdirAll(work, 0700)
		os.WriteFile(filepath.Join(work, "lock"), nil, 0600)
		if err := writeDownloadCache(work, image, []descriptor{{Digest: blob, Size: 6}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RemoveDownloadCache(root, images[0], false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("shared layer removed")
	}
	if _, err := RemoveDownloadCache(root, images[1], true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("preview deleted layer")
	}
	if _, err := RemoveDownloadCache(root, images[1], false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unused layer retained")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("untracked layer removed")
	}
}
