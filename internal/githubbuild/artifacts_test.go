package githubbuild

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/release"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSignedArtifactBindsAttemptAndDownloadOrigin(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			testSignedArtifactBindsAttemptAndDownloadOrigin(t, version)
		})
	}
}

func testSignedArtifactBindsAttemptAndDownloadOrigin(t *testing.T, version int) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	m := release.Manifest{Version: 1, ID: "ci-12-2", Build: release.Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: strings.Repeat("a", 40), RunID: 12, RunAttempt: 2}, Images: []release.Image{{Service: "admin-api", Platform: "linux/arm64", Reference: "example/api@sha256:" + strings.Repeat("b", 64)}}}
	m.Version = version
	if version == 2 {
		m.Build.StartedAt = "2026-10-01T10:00:00Z"
	}
	signed, _ := release.Sign(m, key)
	data, _ := json.Marshal(signed)
	var zipBytes bytes.Buffer
	z := zip.NewWriter(&zipBytes)
	f, _ := z.Create("signed-images.json")
	f.Write(data)
	z.Close()
	archive := zipBytes.Bytes()
	sum := sha256.Sum256(archive)
	path := filepath.Join(t.TempDir(), "token")
	os.WriteFile(path, []byte("synthetic-provider-credential"), 0600)
	c, err := New(Config{Repository: "example/tooling", Workflow: "build.yml", AllowedRefs: []string{"main"}, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	c.http = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		status := 200
		headers := http.Header{}
		var value any
		var raw []byte
		if req.URL.Host == "productionresultsfixture.blob.core.windows.net" {
			if req.Header.Get("Authorization") != "" {
				t.Error("provider credential forwarded to storage")
			}
			raw = archive
		} else {
			switch req.URL.Path {
			case "/repos/example/tooling/actions/runs/12":
				value = map[string]any{"id": 12, "run_attempt": 2, "status": "completed", "conclusion": "success", "event": "workflow_dispatch", "head_branch": "main", "head_sha": m.Build.SourceCommit, "workflow_id": 99, "path": ".github/workflows/build.yml", "head_repository": map[string]string{"full_name": "example/tooling"}}
			case "/repos/example/tooling/actions/workflows/build.yml":
				value = map[string]int{"id": 99}
			case "/repos/example/tooling/actions/runs/12/artifacts":
				value = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 22, "name": "skygo-admin-signed-images-2", "digest": "sha256:" + hex.EncodeToString(sum[:]), "size_in_bytes": len(archive), "workflow_run": map[string]any{"id": 12, "head_sha": m.Build.SourceCommit}}}}
			case "/repos/example/tooling/actions/artifacts/22/zip":
				status = 302
				headers.Set("Location", "https://productionresultsfixture.blob.core.windows.net/archive")
			default:
				t.Errorf("unexpected provider path %s", req.URL.Path)
				status = 404
			}
		}
		if raw == nil {
			raw, _ = json.Marshal(value)
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(raw)), Request: req}, nil
	})}
	artifact, err := c.SignedImages(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := release.Verify(artifact.Release, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)})
	if err != nil || artifact.Run.Attempt != 2 || !artifact.Matches(verified, c.Config()) {
		t.Fatal("attempt provenance mismatch")
	}
	for name, mutate := range map[string]func(*release.Manifest){
		"unsupported schema": func(m *release.Manifest) { m.Version = 3 },
		"invalid build time": func(m *release.Manifest) { m.Build.StartedAt = "invalid" },
		"repository":         func(m *release.Manifest) { m.Build.Repository = "example/other" },
		"workflow":           func(m *release.Manifest) { m.Build.Workflow = "other.yml" },
		"branch":             func(m *release.Manifest) { m.Build.Ref = "refs/heads/other" },
		"commit":             func(m *release.Manifest) { m.Build.SourceCommit = strings.Repeat("c", 40) },
		"run":                func(m *release.Manifest) { m.Build.RunID = 13; m.ID = "ci-13-2" },
	} {
		changed := verified
		mutate(&changed)
		if artifact.Matches(changed, c.Config()) {
			t.Errorf("accepted mismatched %s", name)
		}
	}
	m.Build.RunAttempt = 3
	if artifact.Matches(m, c.Config()) {
		t.Fatal("different attempt accepted")
	}
	if _, err = c.downloadArtifact(context.Background(), "https://example.com/archive", 1); err == nil {
		t.Fatal("unapproved download origin accepted")
	}
}
