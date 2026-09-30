package registrycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	opsagent "github.com/alpha2z/skygo-admin/internal/transfer"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func registryFixture(t *testing.T, corrupt bool, labels ...map[string]string) (*Client, ops.ReleaseImage) {
	t.Helper()
	return registryFixtureWithLayer(t, corrupt, []byte("fixture layer bytes"), labels...)
}
func registryFixtureWithLayer(t *testing.T, corrupt bool, layer []byte, labels ...map[string]string) (*Client, ops.ReleaseImage) {
	t.Helper()
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	z.Write(layer)
	z.Close()
	settings := map[string]any{"os": "linux", "architecture": "arm64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{digest(layer)}}}
	if len(labels) > 0 {
		settings["config"] = map[string]any{"Labels": labels[0]}
	}
	config, _ := json.Marshal(settings)
	m := manifest{SchemaVersion: 2, Config: descriptor{Digest: digest(config), Size: int64(len(config)), MediaType: "application/vnd.oci.image.config.v1+json"}, Layers: []descriptor{{Digest: digest(compressed.Bytes()), Size: int64(compressed.Len()), MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}}}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": m.SchemaVersion, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": m.Config, "layers": m.Layers})
	image := ops.ReleaseImage{Role: "admin-web", Reference: "ghcr.io/owner/admin-web@" + digest(raw), Platform: "linux/arm64", ImageID: digest(config)}
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("fixture-secret"), 0600)
	client := &Client{Username: "fixture", TokenFile: token, Packages: []string{"owner/admin-web"}}
	client.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch {
		case r.URL.Path == "/token":
			user, password, ok := r.BasicAuth()
			if !ok || user != "fixture" || password != "fixture-secret" || r.URL.Query().Get("scope") != "repository:owner/admin-web:pull" {
				t.Fatal("invalid credential scope")
			}
			body = []byte(`{"token":"scoped-token"}`)
		default:
			if r.Header.Get("Authorization") != "Bearer scoped-token" {
				t.Fatal("missing scoped token")
			}
			switch {
			case strings.HasSuffix(r.URL.Path, digest(raw)):
				body = raw
			case strings.HasSuffix(r.URL.Path, digest(config)):
				body = config
			case strings.HasSuffix(r.URL.Path, digest(compressed.Bytes())):
				body = append([]byte{}, compressed.Bytes()...)
				if corrupt {
					body[len(body)-1] ^= 1
				}
			default:
				t.Fatal("unexpected request")
			}
		}
		return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return client, image
}
func TestDownloadVerifiedDockerArchive(t *testing.T) {
	client, image := registryFixture(t, false)
	result, err := client.Download(context.Background(), image, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = opsagent.ValidateArchiveImage(f, image); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, 0)
	reader := tar.NewReader(f)
	layers := 0
	for {
		h, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasSuffix(h.Name, "layer.tar") {
			layers++
		}
	}
	if layers != 1 || result.Size <= 0 || len(result.SHA256) != 64 {
		t.Fatal("missing archive evidence")
	}
}
func TestDownloadRejectsCorruptLayerAndUnauthorizedPackage(t *testing.T) {
	client, image := registryFixture(t, true)
	dir := t.TempDir()
	if _, err := client.Download(context.Background(), image, dir, nil); err == nil {
		t.Fatal("accepted corrupted layer")
	}
	files, _ := os.ReadDir(dir)
	for _, file := range files {
		if !file.IsDir() {
			t.Fatal("partial archive published")
		}
	}
	client.Packages = []string{"owner/admin-api"}
	if _, err := client.Download(context.Background(), image, dir, nil); err == nil {
		t.Fatal("accepted unapproved package")
	}
}
func TestRegistryRedirectRemovesCredentials(t *testing.T) {
	c := (&Client{}).httpClient()
	r, _ := http.NewRequest("GET", "https://pkg-containers.githubusercontent.com/blob", nil)
	r.Header.Set("Authorization", "secret")
	if err := c.CheckRedirect(r, nil); err != nil || r.Header.Get("Authorization") != "" {
		t.Fatal("credential forwarded")
	}
	r, _ = http.NewRequest("GET", "https://attacker.example/blob", nil)
	if c.CheckRedirect(r, nil) == nil {
		t.Fatal("unapproved redirect accepted")
	}
}

func TestIndexKeepsSignedRootAndResolvesOnlyMatchingPlatform(t *testing.T) {
	client, image := registryFixture(t, false)
	selected := strings.Split(image.Reference, "@")[1]
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{"digest": selected, "platform": map[string]string{"os": "linux", "architecture": "arm64"}}}})
	rootID := digest(index)
	image.Reference = "ghcr.io/owner/admin-web@" + rootID
	base := client.HTTP.Transport
	client.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, rootID) {
			return &http.Response{StatusCode: 200, ContentLength: int64(len(index)), Body: io.NopCloser(bytes.NewReader(index)), Header: make(http.Header)}, nil
		}
		return base.RoundTrip(r)
	})
	result, err := client.Download(context.Background(), image, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	runtimeID, err := opsagent.ArchiveRuntimeImage(file, image)
	if err != nil || runtimeID != selected {
		t.Fatal("index chain not retained", err)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	altered := bytes.Replace(raw, index, bytes.Replace(index, []byte("arm64"), []byte("amd64"), 1), 1)
	if _, err = opsagent.ArchiveRuntimeImage(bytes.NewReader(altered), image); err == nil {
		t.Fatal("modified signed index accepted")
	}
}

func TestDownloadProgressCountsCompressedBytesNotArchiveExpansion(t *testing.T) {
	client, image := registryFixture(t, false)
	directory := t.TempDir()
	var data bytes.Buffer
	z := gzip.NewWriter(&data)
	z.Write([]byte("fixture layer bytes"))
	z.Close()
	for pass := 0; pass < 2; pass++ {
		var progress, reused int64
		_, err := client.Download(context.Background(), image, directory, func(n int64) { progress = n }, func(n int64, _ string) { reused = n })
		if err != nil {
			t.Fatal(err)
		}
		if progress != int64(data.Len()) {
			t.Fatalf("expanded archive bytes reported as network bytes: %d want %d", progress, data.Len())
		}
		if pass == 1 && progress-reused != 0 {
			t.Fatalf("cached layer reported as downloaded: progress %d reused %d", progress, reused)
		}
	}
}

func TestTokyoTagSurvivesArchiveForDockerAndContainerd(t *testing.T) {
	labels := map[string]string{"org.opencontainers.image.created": "2026-09-29T23:15:00Z", "io.skygo-admin.build.version": "20260930-081500-arm64", "io.skygo-admin.build.timezone": "Asia/Tokyo"}
	client, image := registryFixture(t, false, labels)
	result, err := client.Download(context.Background(), image, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := tar.NewReader(f)
	found := 0
	for {
		h, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		switch h.Name {
		case "manifest.json":
			var manifest []struct{ RepoTags []string }
			if json.NewDecoder(reader).Decode(&manifest) != nil || len(manifest) != 1 || len(manifest[0].RepoTags) != 1 || manifest[0].RepoTags[0] != "skygo-admin/admin-web:20260930-081500-arm64" {
				t.Fatal("Docker timestamp tag missing")
			}
			found++
		case "index.json":
			var index struct {
				Manifests []struct{ Annotations map[string]string }
			}
			if json.NewDecoder(reader).Decode(&index) != nil || len(index.Manifests) != 1 || index.Manifests[0].Annotations["io.containerd.image.name"] != "docker.io/skygo-admin/admin-web:20260930-081500-arm64" {
				t.Fatal("containerd timestamp tag missing")
			}
			found++
		}
	}
	if found != 2 {
		t.Fatal("archive manifests missing")
	}
	labels["io.skygo-admin.build.version"] = "20260930-071500-arm64"
	if _, err := readableImageTag(image, labels); err == nil {
		t.Fatal("wrong time zone accepted")
	}
	if tag, err := readableImageTag(image, nil); err != nil || tag != "" {
		t.Fatal("historical image was retagged")
	}
}
