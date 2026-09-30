package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/imagecontract"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"github.com/alpha2z/skygo-admin/internal/transfer"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type authenticatedTransport struct {
	base                http.RoundTripper
	token, host, origin string
}

func (t authenticatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != t.origin {
		return nil, errors.New("archive origin rejected")
	}
	req := r.Clone(r.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("X-Host-ID", t.host)
	return t.base.RoundTrip(req)
}

type imageEvidence struct{ Reference, Platform, RuntimeID string }

func evidencePath(s LocalService, ref string) string {
	return filepath.Join(s.CacheDir, control.Digest([]byte(ref))+".identity")
}
func preparedImage(ctx context.Context, s LocalService, ref string) (imageDetails, error) {
	image, err := inspectImage(ctx, ref)
	if err == nil {
		return image, nil
	}
	if s.CacheDir == "" {
		return image, err
	}
	raw, e := os.ReadFile(evidencePath(s, ref))
	var evidence imageEvidence
	if e != nil || json.Unmarshal(raw, &evidence) != nil || evidence.Reference != ref || !control.ImageIdentity(evidence.RuntimeID) {
		return image, err
	}
	image, e = inspectImage(ctx, evidence.RuntimeID)
	if e != nil || image.ID != evidence.RuntimeID || image.platform() != evidence.Platform {
		return image, errors.New("prepared image changed")
	}
	return image, nil
}
func (a *Agent) execute(ctx context.Context, s LocalService, c control.Command) control.Result {
	if c.Action == "extension" {
		if x, ok := a.extension(s, c.Extension); ok {
			return x.Execute(ctx, s, c)
		}
		return control.Result{ID: c.ID, Status: "failed", Code: "EXTENSION_UNAVAILABLE"}
	}

	if c.Action == "image-cleanup" {
		return a.executeCleanup(ctx, s, c)
	}
	if c.Action != "prepare-image" {
		return a.driver.Execute(ctx, s, c)
	}
	var attempt struct {
		Attempt uint64 `json:"attempt"`
	}
	if c.DeliveryID != "" {
		if a.request(ctx, "POST", "/image-deliveries/"+c.DeliveryID+"/attempts", map[string]string{"task_id": c.ID}, &attempt) != nil {
			return control.Result{ID: c.ID, Status: "failed", Code: "ATTEMPT_UNAVAILABLE"}
		}
	}
	var sequence uint64
	var reused, downloaded int64
	var last time.Time
	report := func(phase string, bytes, total int64, reason string, force bool) {
		if c.DeliveryID == "" || (!force && time.Since(last) < time.Second) {
			return
		}
		last = time.Now()
		sequence++
		work, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		_ = a.request(work, "POST", "/image-deliveries/"+c.DeliveryID+"/progress", control.Progress{TaskID: c.ID, Attempt: attempt.Attempt, Sequence: sequence, Phase: phase, Bytes: bytes, Total: total, Reused: reused, Downloaded: &downloaded, Reason: reason}, nil)
	}
	if c.Archive == nil {
		report("registry-pull", 0, 0, "", true)
		return a.driver.Execute(ctx, s, c)
	}
	result := control.Result{ID: c.ID, Status: "failed", Code: "ARCHIVE_REJECTED"}
	allowed := false
	for _, repo := range s.AllowedImages {
		allowed = allowed || repo == strings.Split(c.Image, "@")[0]
	}
	if !allowed {
		return result
	}
	if _, ok := a.driver.(Docker); !ok {
		return result
	}
	directory := filepath.Join(a.cfg.StateDir, "images")
	if os.MkdirAll(directory, 0700) != nil {
		return result
	}
	client := &http.Client{Transport: authenticatedTransport{base: http.DefaultTransport, token: a.token, host: a.cfg.HostID, origin: strings.TrimRight(a.cfg.APIURL, "/")}}
	source := transfer.ImageSource{Directory: directory, Origin: strings.TrimRight(a.cfg.APIURL, "/"), HostID: a.cfg.HostID, Client: client, DeliveryID: c.DeliveryID,
		Transferred: func(n int64) { downloaded += n }, Resume: func(n int64, reason string) { reused = n; report("downloading", n, c.Archive.Size, reason, true) }, Progress: func(phase string, n, total int64) { report(phase, n, total, "", false) }}
	path, err := source.DownloadArtifact(ctx, c.Archive.SHA256, c.Archive.Size)
	if err != nil {
		result.Code = imagecontract.FailureCode(err)
		return result
	}
	image := imagecontract.ReleaseImage{Role: s.ID, Platform: c.Platform, Reference: c.Image, ImageID: c.Archive.ImageID}
	report("importing", c.Archive.Size, c.Archive.Size, "", true)
	id, err := transfer.ImportReleaseArchive(ctx, func(ctx context.Context, args ...string) ([]byte, error) { return run(ctx, os.Environ(), args...) }, path, image)
	if err != nil {
		result.Code = imagecontract.FailureCode(err)
		return result
	}
	raw, _ := json.Marshal(imageEvidence{Reference: c.Image, Platform: c.Platform, RuntimeID: id})
	if settings.Atomic(evidencePath(s, c.Image), raw) != nil {
		result.Code = "JOURNAL_UNAVAILABLE"
		return result
	}
	result.Status = "succeeded"
	result.Code = "IMAGE_PREPARED"
	result.Image = c.Image
	result.Platform = c.Platform
	result.ImageID = id
	report("ready", c.Archive.Size, c.Archive.Size, "", true)
	return result
}

func (a *Agent) executeCleanup(ctx context.Context, s LocalService, c control.Command) control.Result {
	r := control.Result{ID: c.ID, Status: "failed", Code: "CLEANUP_PROTECTED"}
	if _, ok := a.driver.(Docker); !ok || c.Cleanup == nil {
		return r
	}
	allowed := false
	for _, repo := range s.AllowedImages {
		allowed = allowed || repo == strings.Split(c.Image, "@")[0]
	}
	if !allowed {
		return r
	}
	journals, _ := filepath.Glob(filepath.Join(a.cfg.StateDir, "*.unit.json"))
	deployments, _ := filepath.Glob(filepath.Join(a.cfg.StateDir, "*.deploy.json"))
	journals = append(journals, deployments...)
	for _, path := range journals {
		b, err := os.ReadFile(path)
		var j unitJournal
		if err != nil || json.Unmarshal(b, &j) != nil || j.Phase != "terminal" {
			return r
		}
	}
	dir := filepath.Join(a.cfg.StateDir, "images")
	if os.MkdirAll(dir, 0700) != nil {
		return r
	}
	result, err := transfer.ImageCleanup(ctx, imagecontract.ImageCleanup{Kind: c.Cleanup.Kind, Image: imagecontract.ReleaseImage{Role: s.ID, Reference: c.Image, Platform: c.Cleanup.Platform, ImageID: c.Cleanup.ImageID}, ArchiveSHA256: c.Cleanup.SHA256}, dir, func(ctx context.Context, args ...string) ([]byte, error) { return run(ctx, os.Environ(), args...) })
	if err != nil {
		r.Code = imagecontract.FailureCode(err)
		return r
	}
	if result.Status == "deleted" || result.Status == "absent" {
		r.Status = "succeeded"
		r.Code = "IMAGE_CLEANED"
		r.Image = c.Image
		r.ImageID = c.Cleanup.ImageID
		r.Platform = c.Cleanup.Platform
	}
	return r
}

func preparedEvidence(s LocalService, ref, id, platform string) bool {
	if s.CacheDir == "" {
		return false
	}
	b, err := os.ReadFile(evidencePath(s, ref))
	var evidence imageEvidence
	return err == nil && json.Unmarshal(b, &evidence) == nil && evidence.Reference == ref && evidence.RuntimeID == id && evidence.Platform == platform
}
