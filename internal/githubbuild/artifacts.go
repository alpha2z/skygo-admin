package githubbuild

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/alpha2z/skygo-admin/internal/release"
)

type artifactRedirect struct{ location string }
type SignedArtifact struct {
	Release release.SignedManifest
	Run     Run
	Attempt int
}

var artifactHost = regexp.MustCompile(`^productionresults[a-z0-9]+\.blob\.core\.windows\.net$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func artifactFailure(code string) error { return &Error{Code: code, Status: 502} }

// SignedImages retrieves data only. Callers must verify the build signature and
// match its provenance to this independently retrieved run before persisting it.
func (c *Client) SignedImages(ctx context.Context, id int64) (SignedArtifact, error) {
	var result SignedArtifact
	if id <= 0 {
		return result, artifactFailure("GITHUB_RUN_NOT_APPROVED")
	}
	var run struct {
		Run
		WorkflowID     int64  `json:"workflow_id"`
		Path           string `json:"path"`
		HeadRepository struct {
			FullName string `json:"full_name"`
		} `json:"head_repository"`
	}
	base := "/repos/" + c.config.Repository + "/actions"
	if err := c.request(ctx, "GET", fmt.Sprintf("%s/runs/%d", base, id), nil, &run); err != nil {
		return result, err
	}
	approved := false
	for _, ref := range c.config.AllowedRefs {
		if ref == run.Ref {
			approved = true
		}
	}
	if !approved || run.ID != id || run.Status != "completed" || run.Conclusion == nil || *run.Conclusion != "success" || (run.Event != "push" && run.Event != "workflow_dispatch") || run.Attempt < 1 || run.Attempt > 10000 || !commitPattern.MatchString(run.Commit) || run.HeadRepository.FullName != c.config.Repository || run.Path != ".github/workflows/"+c.config.Workflow {
		return result, artifactFailure("GITHUB_RUN_NOT_APPROVED")
	}
	var workflow struct {
		ID int64 `json:"id"`
	}
	if err := c.request(ctx, "GET", c.path(""), nil, &workflow); err != nil {
		return result, err
	}
	if workflow.ID <= 0 || workflow.ID != run.WorkflowID {
		return result, artifactFailure("GITHUB_RUN_NOT_APPROVED")
	}
	var list struct {
		Total     int `json:"total_count"`
		Artifacts []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Digest  string `json:"digest"`
			Size    int64  `json:"size_in_bytes"`
			Expired bool   `json:"expired"`
			Run     struct {
				ID     int64  `json:"id"`
				Commit string `json:"head_sha"`
			} `json:"workflow_run"`
		} `json:"artifacts"`
	}
	if err := c.request(ctx, "GET", fmt.Sprintf("%s/runs/%d/artifacts?per_page=100", base, id), nil, &list); err != nil {
		return result, err
	}
	if list.Total > 100 {
		return result, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	artifactID := int64(0)
	digest := ""
	size := int64(0)
	for _, a := range list.Artifacts {
		if a.Name != fmt.Sprintf("skygo-admin-signed-images-%d", run.Attempt) {
			continue
		}
		if artifactID != 0 || a.ID <= 0 || a.Expired || a.Size <= 0 || a.Size > 1<<20 || a.Run.ID != id || a.Run.Commit != run.Commit || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(a.Digest) {
			return result, artifactFailure("GITHUB_ARTIFACT_INVALID")
		}
		artifactID, digest, size = a.ID, a.Digest, a.Size
	}
	if artifactID == 0 {
		return result, artifactFailure("GITHUB_SIGNED_ARTIFACT_MISSING")
	}
	var redirect artifactRedirect
	if err := c.request(ctx, "GET", fmt.Sprintf("%s/artifacts/%d/zip", base, artifactID), nil, &redirect); err != nil {
		return result, err
	}
	raw, err := c.downloadArtifact(ctx, redirect.location, size)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return result, artifactFailure("GITHUB_ARTIFACT_DIGEST_MISMATCH")
	}
	signed, err := decodeImageArchive(raw)
	if err != nil {
		return result, err
	}
	result = SignedArtifact{Release: signed, Run: run.Run, Attempt: run.Attempt}
	return result, nil
}
func (c *Client) downloadArtifact(ctx context.Context, location string, size int64) ([]byte, error) {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || !artifactHost.MatchString(u.Hostname()) || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.Fragment != "" {
		return nil, artifactFailure("GITHUB_ARTIFACT_REDIRECT_REJECTED")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
	if err != nil {
		return nil, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	// No GitHub Authorization on signed storage URLs, and no further redirects.
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, artifactFailure("GITHUB_ARTIFACT_DOWNLOAD_FAILED")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, artifactFailure("GITHUB_ARTIFACT_DOWNLOAD_FAILED")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || int64(len(raw)) != size || len(raw) > 1<<20 {
		return nil, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	return raw, nil
}
func decodeImageArchive(raw []byte) (release.SignedManifest, error) {
	var signed release.SignedManifest
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) != 1 {
		return signed, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	file := archive.File[0]
	if file.Name != "signed-images.json" || !file.Mode().IsRegular() || file.UncompressedSize64 > 128<<10 {
		return signed, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	reader, err := file.Open()
	if err != nil {
		return signed, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, (128<<10)+1))
	if err != nil || len(data) > 128<<10 || json.Unmarshal(data, &signed) != nil {
		return signed, artifactFailure("GITHUB_ARTIFACT_INVALID")
	}
	return signed, nil
}
func (a SignedArtifact) Matches(manifest release.Manifest, config Config) bool {
	b := manifest.Build
	return manifest.Version == 1 && b.Repository == config.Repository && b.Workflow == config.Workflow && b.Ref == "refs/heads/"+a.Run.Ref && b.SourceCommit == a.Run.Commit && b.RunID == a.Run.ID && b.RunAttempt == a.Attempt && manifest.ID == fmt.Sprintf("ci-%d-%d", a.Run.ID, a.Attempt) && strings.HasPrefix(b.Ref, "refs/heads/")
}
