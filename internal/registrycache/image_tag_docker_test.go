package registrycache

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Real Docker import of an archive produced by the central downloader; registry
// HTTP is a deterministic fixture. No existing image or running container changes.
func TestTokyoArchiveRealDockerImport(t *testing.T) {
	if os.Getenv("SKYGO_ADMIN_TAG_DOCKER_TEST") != "1" {
		t.Skip("explicit isolated Docker test required")
	}
	created := time.Now().UTC().Truncate(time.Second)
	version := created.In(time.FixedZone("JST", 9*3600)).Format("20060102-150405") + "-arm64"
	tag := "skygo-admin/admin-web:" + version
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if exec.CommandContext(ctx, "docker", "image", "inspect", tag).Run() == nil {
		t.Skip("timestamp tag already exists")
	}
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	body := []byte("isolated Tokyo image tag verification\n")
	if err := tw.WriteHeader(&tar.Header{Name: "version.txt", Mode: 0644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	labels := map[string]string{"org.opencontainers.image.created": created.Format(time.RFC3339), "io.skygo-admin.build.version": version, "io.skygo-admin.build.timezone": "Asia/Tokyo"}
	client, image := registryFixtureWithLayer(t, false, layer.Bytes(), labels)
	archive, err := client.Download(ctx, image, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("docker", "image", "rm", "--no-prune", tag).Run() })
	if out, err := exec.CommandContext(ctx, "docker", "load", "--input", archive.Path).CombinedOutput(); err != nil {
		t.Fatalf("import failed: %v %s", err, out)
	}
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", tag).Output()
	if err != nil {
		t.Fatal("timestamp tag missing after real Docker import", err)
	}
	var images []struct {
		Os, Architecture string
		Config           struct{ Labels map[string]string }
	}
	if json.Unmarshal(out, &images) != nil || len(images) != 1 || images[0].Architecture != "arm64" || images[0].Config.Labels["io.skygo-admin.build.version"] != version {
		t.Fatal("imported tag has wrong identity")
	}
	t.Log("central archive imported into Docker with verified Tokyo tag", tag)
}
