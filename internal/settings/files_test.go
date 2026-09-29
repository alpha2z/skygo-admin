package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretPermissionsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	if Atomic(path, []byte("synthetic value\n")) != nil {
		t.Fatal("write failed")
	}
	if v, err := Secret(path); err != nil || v != "synthetic value" {
		t.Fatal("private file rejected")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(path, link)
	if _, err := Secret(link); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Chmod(path, 0644)
	if _, err := Secret(path); err == nil {
		t.Fatal("public file accepted")
	}
}
