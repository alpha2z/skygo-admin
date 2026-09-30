package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupProtectsStoppedContainerAndDoesNotDeleteArchive(t *testing.T) {
	data, im := testArchive(t, "linux/amd64")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	dir := t.TempDir()
	path := filepath.Join(dir, digest+".partial")
	os.WriteFile(path, data, 0600)
	plan := ops.ImageCleanup{Kind: "archive", Image: im, ArchiveSHA256: digest}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			return []byte(im.ImageID + " linux/amd64"), nil
		}
		if args[0] == "ps" {
			if strings.Join(args, " ") != "ps -aq --filter ancestor="+im.ImageID {
				t.Fatal(args)
			}
			return []byte("stopped-container"), nil
		}
		t.Fatalf("unexpected Docker mutation: %v", args)
		return nil, nil
	}
	result, err := ImageCleanup(context.Background(), plan, dir, run)
	if err != nil || result.Status != "protected" {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("protected archive deleted")
	}
}
func TestCleanupArchivePreviewThenDeleteIsIdempotent(t *testing.T) {
	data, im := testArchive(t, "linux/amd64")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	dir := t.TempDir()
	path := filepath.Join(dir, digest+".partial")
	os.WriteFile(path, data, 0600)
	plan := ops.ImageCleanup{Kind: "archive", Inspect: true, Image: im, ArchiveSHA256: digest}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "image" {
			return []byte(im.ImageID + " linux/amd64"), nil
		}
		return nil, nil
	}
	result, err := ImageCleanup(context.Background(), plan, dir, run)
	if err != nil || result.Status != "ready" {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("preview deleted resource")
	}
	plan.Inspect = false
	result, err = ImageCleanup(context.Background(), plan, dir, run)
	if err != nil || result.Status != "deleted" || result.Reclaimed != nil || result.Size != int64(len(data)) {
		t.Fatal(result, err)
	}
	result, err = ImageCleanup(context.Background(), plan, dir, run)
	if err != nil || result.Status != "absent" {
		t.Fatal(result, err)
	}
}
