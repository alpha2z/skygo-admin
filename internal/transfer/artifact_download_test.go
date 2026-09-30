package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testArchive(t *testing.T, platform string) ([]byte, ops.ReleaseImage) {
	t.Helper()
	parts := strings.Split(platform, "/")
	config, _ := json.Marshal(map[string]string{"os": parts[0], "architecture": parts[1]})
	sum := sha256.Sum256(config)
	id := hex.EncodeToString(sum[:])
	manifest, _ := json.Marshal([]map[string]any{{"Config": id + ".json", "Layers": []string{}, "RepoTags": []string{}}})
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{{id + ".json", config}, {"manifest.json", manifest}} {
		if err := w.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.data))}); err != nil {
			t.Fatal(err)
		}
		w.Write(f.data)
	}
	w.Close()
	return buf.Bytes(), ops.ReleaseImage{Role: "admin-api", Reference: "ghcr.io/owner/admin-api@sha256:" + strings.Repeat("a", 64), Platform: platform, ImageID: "sha256:" + id}
}
func TestArchiveResumeRangeAndFullIdentity(t *testing.T) {
	for _, platform := range []string{"linux/arm64", "linux/amd64"} {
		t.Run(platform, func(t *testing.T) {
			data, img := testArchive(t, platform)
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, digest+".partial"), data[:700], 0600)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Range") != "bytes=700-" || r.Header.Get("X-Host-ID") != "node-a" || r.URL.Path != "/agent/v1/image-deliveries/job/archive" {
					t.Error("range or grant not sent")
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 700-%d/%d", len(data)-1, len(data)))
				w.Header().Set("Content-Length", fmt.Sprint(len(data)-700))
				w.WriteHeader(206)
				w.Write(data[700:])
			}))
			defer server.Close()
			source := ImageSource{Directory: root, Client: server.Client(), Origin: server.URL, HostID: "node-a", DeliveryID: "job"}
			name, err := source.DownloadArtifact(context.Background(), digest, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			file, _ := os.Open(name)
			defer file.Close()
			if err := ValidateArchiveImage(file, img); err != nil {
				t.Fatal(err)
			}
			if _, err = source.DownloadArtifact(context.Background(), digest, int64(len(data))); err != nil || requests != 1 {
				t.Fatal("verified cache redownloaded", err, requests)
			}
			img.ImageID = "sha256:" + strings.Repeat("b", 64)
			if ValidateArchiveImage(file, img) == nil {
				t.Fatal("foreign config accepted")
			}
		})
	}
}
func TestArchiveDisconnectRetainsPartialAndRejectsCorruption(t *testing.T) {
	data, _ := testArchive(t, "linux/arm64")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	root := t.TempDir()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Write(data[:900])
			return
		}
		http.ServeContent(w, r, "archive", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	source := ImageSource{Directory: root, Origin: server.URL, HostID: "node", Client: server.Client()}
	if _, err := source.DownloadArtifact(context.Background(), digest, int64(len(data))); err == nil {
		t.Fatal("partial accepted")
	}
	info, _ := os.Stat(filepath.Join(root, digest+".partial"))
	if info.Size() != 900 {
		t.Fatal("partial not retained")
	}
	if _, err := source.DownloadArtifact(context.Background(), digest, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	source.Directory = t.TempDir()
	bad := strings.Repeat("c", 64)
	if _, err := source.DownloadArtifact(context.Background(), bad, int64(len(data))); ops.FailureCode(err) != "DIGEST_MISMATCH" {
		t.Fatal(err)
	}
}

func TestContainerdAndClassicRuntimeIdentitiesRemainBound(t *testing.T) {
	_, image := testArchive(t, "linux/arm64")
	manifest := strings.Split(image.Reference, "@")[1]
	for _, identity := range []string{image.ImageID, manifest} {
		run := func(ctx context.Context, args ...string) ([]byte, error) {
			if args[len(args)-1] != identity {
				return nil, fmt.Errorf("not present")
			}
			return []byte(identity + " " + image.Platform), nil
		}
		got, err := VerifyReleaseImage(context.Background(), run, image)
		if err != nil || got != identity {
			t.Fatal(got, err)
		}
	}
	malicious := func(context.Context, ...string) ([]byte, error) {
		return []byte("sha256:" + strings.Repeat("f", 64) + " linux/arm64"), nil
	}
	if _, err := VerifyReleaseImage(context.Background(), malicious, image); err == nil {
		t.Fatal("untrusted runtime image accepted")
	}
}

func TestArchiveRangeResetResponses(t *testing.T) {
	for _, mode := range []string{"ignored", "unsatisfiable", "wrong-range"} {
		t.Run(mode, func(t *testing.T) {
			data, _ := testArchive(t, "linux/amd64")
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, digest+".partial"), data[:700], 0600)
			requests := 0
			reason := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 && r.Header.Get("Range") != "bytes=700-" {
					t.Error("initial range missing")
				}
				switch mode {
				case "unsatisfiable":
					if requests == 1 {
						w.WriteHeader(416)
						return
					}
					if r.Header.Get("Range") != "" {
						t.Error("reset request retained range")
					}
				case "wrong-range":
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 699-%d/%d", len(data)-1, len(data)))
					w.Header().Set("Content-Length", fmt.Sprint(len(data)-699))
					w.WriteHeader(206)
					w.Write(data[699:])
					return
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				w.Write(data)
			}))
			defer server.Close()
			source := ImageSource{Directory: root, Client: server.Client(), Origin: server.URL, HostID: "node", Resume: func(_ int64, s string) { reason = s }}
			file, err := source.DownloadArtifact(context.Background(), digest, int64(len(data)))
			if mode == "wrong-range" {
				if err == nil {
					t.Fatal("invalid range accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(file)
			if !bytes.Equal(raw, data) {
				t.Fatal("reset appended corrupt bytes")
			}
			expected := "range_not_supported"
			if mode == "unsatisfiable" {
				expected = "range_rejected"
			}
			if reason != expected {
				t.Fatal(reason)
			}
		})
	}
}
