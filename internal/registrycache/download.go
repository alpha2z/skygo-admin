// Package opsregistry downloads approved GHCR images on the Admin host only.
// It does not require a Docker socket and never executes image contents.
package registrycache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	opsagent "github.com/alpha2z/skygo-admin/internal/transfer"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maxArchive int64 = 8 << 30
const maxMetadata int64 = 2 << 20

type Client struct {
	MaxCacheBytes       int64
	Username, TokenFile string
	Packages            []string
	HTTP                *http.Client
}

// Keep a reserve while streaming; declared compressed size cannot predict
// uncompressed layers, so checking only before a download is insufficient.
type spaceWriter struct {
	quota     int64
	writer    io.Writer
	directory string
	remaining int64
}

func (w *spaceWriter) Write(p []byte) (int, error) {
	if w.remaining < int64(len(p)) {
		if err := opsagent.CheckCapacityLimit(w.directory, int64(len(p)), w.quota); err != nil {
			return 0, err
		}
		if err := opsagent.RequireFreeSpace(w.directory, 128<<20+uint64(len(p))); err != nil {
			return 0, err
		}
		w.remaining = 16 << 20
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

type Archive struct {
	Path, SHA256   string
	ImageID        string
	BuildStartedAt string
	Size           int64
}
type descriptor struct {
	Digest    string                            `json:"digest"`
	Size      int64                             `json:"size"`
	MediaType string                            `json:"mediaType"`
	Platform  struct{ OS, Architecture string } `json:"platform"`
}
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
	Manifests     []descriptor `json:"manifests"`
}

func digest(raw []byte) string { s := sha256.Sum256(raw); return "sha256:" + hex.EncodeToString(s[:]) }
func validDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	raw, err := hex.DecodeString(s[7:])
	return err == nil && len(raw) == 32 && strings.ToLower(s) == s
}
func failure(status int) error {
	switch status {
	case 401:
		return ops.Fail("REGISTRY_AUTH_FAILED")
	case 403:
		return ops.Fail("PACKAGE_ACCESS_DENIED")
	case 429:
		return ops.Fail("RATE_LIMITED")
	case 404:
		return ops.Fail("IMAGE_UNAVAILABLE")
	}
	return ops.Fail("NETWORK_TIMEOUT")
}
func (c *Client) httpClient() *http.Client {
	client := &http.Client{Timeout: 15 * time.Minute}
	if c.HTTP != nil {
		*client = *c.HTTP
	}
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || r.URL.Scheme != "https" || r.URL.User != nil {
			return errors.New("registry redirect rejected")
		}
		host := r.URL.Hostname()
		if r.URL.Port() != "" || (host != "ghcr.io" && host != "pkg-containers.githubusercontent.com" && !strings.HasSuffix(host, ".blob.core.windows.net")) {
			return errors.New("registry redirect rejected")
		}
		r.Header.Del("Authorization")
		return nil
	}
	return client
}
func (c *Client) token(ctx context.Context, repository string) (string, error) {
	approved := false
	for _, p := range c.Packages {
		if p == repository {
			approved = true
		}
	}
	if !approved || c.Username == "" {
		return "", errors.New("registry package not approved")
	}
	info, err := os.Lstat(c.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return "", errors.New("private Admin registry token required")
	}
	raw, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return "", errors.New("Admin registry token unavailable")
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("invalid Admin registry token")
	}
	endpoint := "https://ghcr.io/token?service=ghcr.io&scope=" + url.QueryEscape("repository:"+repository+":pull")
	req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	req.SetBasicAuth(c.Username, secret)
	// Token requests must never redirect credentials or accept a foreign issuer.
	client := c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", ops.Fail("NETWORK_TIMEOUT")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", failure(resp.StatusCode)
	}
	var result struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 32768)).Decode(&result) != nil {
		return "", errors.New("invalid registry authorization response")
	}
	if result.Token == "" {
		result.Token = result.AccessToken
	}
	if result.Token == "" || len(result.Token) > 16000 {
		return "", errors.New("invalid registry authorization response")
	}
	return result.Token, nil
}
func (c *Client) get(ctx context.Context, repository, kind, id, token string) (*http.Response, error) {
	return c.getOffset(ctx, repository, kind, id, token, 0)
}
func (c *Client) getOffset(ctx context.Context, repository, kind, id, token string, offset int64) (*http.Response, error) {
	if !validDigest(id) {
		return nil, ops.Fail("DIGEST_MISMATCH")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://ghcr.io/v2/"+repository+"/"+kind+"/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, ops.Fail("NETWORK_TIMEOUT")
	}
	if resp.StatusCode != 200 && !(offset > 0 && (resp.StatusCode == 206 || resp.StatusCode == 416)) {
		resp.Body.Close()
		return nil, failure(resp.StatusCode)
	}
	return resp, nil
}
func (c *Client) metadata(ctx context.Context, repository, kind, id, token string) ([]byte, error) {
	resp, err := c.get(ctx, repository, kind, id, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, ops.Fail("NETWORK_TIMEOUT")
	}
	if int64(len(raw)) > maxMetadata || digest(raw) != id {
		return nil, ops.Fail("DIGEST_MISMATCH")
	}
	return raw, nil
}
func (c *Client) Download(ctx context.Context, image ops.ReleaseImage, directory string, progress func(int64), resume ...func(int64, string)) (Archive, error) {
	var result Archive
	if image.Validate() != nil || !filepath.IsAbs(directory) {
		return result, errors.New("invalid image download")
	}
	parts := strings.Split(strings.TrimPrefix(image.Reference, "ghcr.io/"), "@")
	repository, id := parts[0], parts[1]
	token, err := c.token(ctx, repository)
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return result, err
	}
	work := filepath.Join(directory, ".registry-"+image.Key())
	if err := os.MkdirAll(work, 0700); err != nil {
		return result, err
	}
	lock, err := os.OpenFile(filepath.Join(work, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return result, errors.New("image download already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entries, err := os.ReadDir(work)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "image.tar" || strings.HasPrefix(name, "compressed-") || strings.HasPrefix(name, "layer-") {
			if err = os.Remove(filepath.Join(work, name)); err != nil {
				return result, err
			}
		}
	}
	var reusedTotal int64
	onResume := func(n int64, reason string) {
		reusedTotal += n
		if len(resume) > 0 && resume[0] != nil {
			resume[0](reusedTotal, reason)
		}
	}
	raw, err := c.metadata(ctx, repository, "manifests", id, token)
	if err != nil {
		return result, err
	}
	rootManifest := append([]byte(nil), raw...)
	rootDigest := id
	var m manifest
	if json.Unmarshal(raw, &m) != nil || m.SchemaVersion != 2 {
		return result, errors.New("unsupported image manifest")
	}
	if len(m.Manifests) > 0 {
		selected := ""
		for _, d := range m.Manifests {
			if d.Platform.OS+"/"+d.Platform.Architecture == image.Platform {
				if selected != "" {
					return result, errors.New("ambiguous image platform")
				}
				selected = d.Digest
			}
		}
		if selected == "" {
			return result, ops.Fail("ARCH_MISMATCH")
		}
		id = selected
		raw, err = c.metadata(ctx, repository, "manifests", selected, token)
		if err != nil {
			return result, err
		}
		m = manifest{}
		if json.Unmarshal(raw, &m) != nil || m.SchemaVersion != 2 || len(m.Manifests) > 0 {
			return result, errors.New("unsupported platform manifest")
		}
	}
	if !validDigest(m.Config.Digest) || (image.ImageID != "" && m.Config.Digest != image.ImageID) || len(m.Layers) > 256 {
		return result, ops.Fail("DIGEST_MISMATCH")
	}
	image.ImageID = m.Config.Digest
	config, err := c.metadata(ctx, repository, "blobs", m.Config.Digest, token)
	if err != nil {
		return result, err
	}
	var settings struct {
		OS, Architecture string
		Config           struct{ Labels map[string]string }
		RootFS           struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if json.Unmarshal(config, &settings) != nil || settings.OS+"/"+settings.Architecture != image.Platform {
		return result, ops.Fail("ARCH_MISMATCH")
	}
	if settings.RootFS.Type != "layers" || len(settings.RootFS.DiffIDs) != len(m.Layers) {
		return result, ops.Fail("DIGEST_MISMATCH")
	}
	tag, err := readableImageTag(image, settings.Config.Labels)
	if err != nil {
		return result, err
	}
	tags := []string{}
	if tag != "" {
		tags = append(tags, tag)
	}
	f, err := os.OpenFile(filepath.Join(work, "image.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	defer f.Close()
	tw := tar.NewWriter(&spaceWriter{writer: f, directory: directory, quota: c.MaxCacheBytes})
	write := func(name string, reader io.Reader, size int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size}); err != nil {
			return err
		}
		_, err := io.CopyN(tw, reader, size)
		return err
	}
	if rootDigest != id {
		if err = write("blobs/sha256/"+rootDigest[7:], strings.NewReader(string(rootManifest)), int64(len(rootManifest))); err != nil {
			return result, err
		}
	}
	configName := "blobs/sha256/" + image.ImageID[7:]
	if err = write(configName, strings.NewReader(string(config)), int64(len(config))); err != nil {
		return result, err
	}
	if err = write("blobs/sha256/"+id[7:], strings.NewReader(string(raw)), int64(len(raw))); err != nil {
		return result, err
	}
	layout := `{"imageLayoutVersion":"1.0.0"}`
	if err = write("oci-layout", strings.NewReader(layout), int64(len(layout))); err != nil {
		return result, err
	}
	entry := map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": id, "size": len(raw), "platform": map[string]string{"os": settings.OS, "architecture": settings.Architecture}}
	if tag != "" {
		entry["annotations"] = map[string]string{"io.containerd.image.name": "docker.io/" + tag, "org.opencontainers.image.ref.name": strings.SplitN(tag, ":", 2)[1]}
	}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{entry}})
	if err = write("index.json", strings.NewReader(string(index)), int64(len(index))); err != nil {
		return result, err
	}
	if err = writeDownloadCache(work, image, m.Layers); err != nil {
		return result, err
	}
	names := []string{}
	var total int64
	var transferredLayers int64
	for i, d := range m.Layers {
		if !validDigest(settings.RootFS.DiffIDs[i]) || d.Size <= 0 || d.Size > maxArchive-total {
			return result, ops.Fail("DIGEST_MISMATCH")
		}
		blobPath, err := c.blob(ctx, repository, d, token, directory, func(n int64) {
			if progress != nil {
				progress(transferredLayers + n)
			}
		}, onResume)
		if err != nil {
			return result, err
		}
		blobFile, err := os.Open(blobPath)
		if err != nil {
			return result, err
		}
		resp := &http.Response{Body: blobFile}
		hash := sha256.New()
		limited := &io.LimitedReader{R: resp.Body, N: d.Size + 1}
		compressedFile, fileErr := os.CreateTemp(work, "compressed-")
		if fileErr != nil {
			resp.Body.Close()
			return result, fileErr
		}
		defer compressedFile.Close()
		compressed := io.TeeReader(limited, io.MultiWriter(hash, &spaceWriter{writer: compressedFile, directory: directory, quota: c.MaxCacheBytes}))
		var source io.Reader = compressed
		var gz *gzip.Reader
		switch d.MediaType {
		case "application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.docker.image.rootfs.diff.tar.gzip":
			gz, err = gzip.NewReader(compressed)
			if err == nil {
				source = gz
			}
		case "application/vnd.oci.image.layer.v1.tar":
		default:
			err = errors.New("unsupported image layer compression")
		}
		if err != nil {
			resp.Body.Close()
			return result, err
		}
		layer, err := os.CreateTemp(work, "layer-")
		if err != nil {
			resp.Body.Close()
			return result, err
		}
		diff := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(&spaceWriter{writer: layer, directory: directory, quota: c.MaxCacheBytes}, diff), io.LimitReader(source, maxArchive-total-d.Size+1))
		if gz != nil {
			gz.Close()
		}
		_, drainErr := io.Copy(io.Discard, compressed)
		resp.Body.Close()
		if copyErr != nil || drainErr != nil {
			layer.Close()
			if copyErr != nil && ops.FailureCode(copyErr) != "OPERATION_FAILED" {
				return result, copyErr
			}
			return result, ops.Fail("DIGEST_MISMATCH")
		}
		if n+d.Size > maxArchive-total {
			layer.Close()
			return result, errors.New("image exceeds archive quota")
		}
		if limited.N != 1 || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != d.Digest || "sha256:"+hex.EncodeToString(diff.Sum(nil)) != settings.RootFS.DiffIDs[i] {
			layer.Close()
			return result, ops.Fail("DIGEST_MISMATCH")
		}
		total += n + d.Size
		transferredLayers += d.Size
		if _, err = compressedFile.Seek(0, 0); err != nil {
			layer.Close()
			return result, err
		}
		if err = write("blobs/sha256/"+d.Digest[7:], compressedFile, d.Size); err != nil {
			layer.Close()
			return result, err
		}
		compressedFile.Close()
		os.Remove(compressedFile.Name())
		name := fmt.Sprintf("%03d/layer.tar", i)
		names = append(names, name)
		if _, err = layer.Seek(0, 0); err == nil {
			err = write(name, layer, n)
		}
		layer.Close()
		os.Remove(layer.Name())
		if err != nil {
			return result, err
		}
		if progress != nil {
			progress(transferredLayers)
		}
	}
	manifestJSON, _ := json.Marshal([]any{map[string]any{"Config": configName, "RepoTags": tags, "Layers": names}})
	if err = write("manifest.json", strings.NewReader(string(manifestJSON)), int64(len(manifestJSON))); err != nil {
		return result, err
	}
	if err = opsagent.CheckArtifactCapacity(directory, 0); err != nil {
		return result, err
	}
	if err = tw.Close(); err != nil {
		return result, err
	}
	if err = f.Sync(); err != nil {
		return result, err
	}
	if _, err = f.Seek(0, 0); err != nil {
		return result, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return result, err
	}
	if size > maxArchive {
		return result, errors.New("image exceeds archive quota")
	}
	result = Archive{ImageID: image.ImageID, BuildStartedAt: settings.Config.Labels["org.opencontainers.image.created"], SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}
	result.Path = filepath.Join(directory, result.SHA256)
	if err = f.Close(); err != nil {
		return Archive{}, err
	}
	if err = os.Rename(filepath.Join(work, "image.tar"), result.Path); err != nil {
		return Archive{}, err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return Archive{}, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return Archive{}, err
	}
	return result, nil
}
