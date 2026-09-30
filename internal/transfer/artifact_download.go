package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
)

// DownloadArtifact resumes only immutable, bounded archives. Partial files are
// retained after disconnect, but are never passed to Docker before full hashing.
func (s *ImageSource) DownloadArtifact(ctx context.Context, digest string, expected int64) (string, error) {
	if len(digest) != 64 {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if !filepath.IsAbs(s.Directory) || s.Client == nil || expected < 0 || expected > MaxArchiveBytes {
		return "", errors.New("artifact download not configured")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return "", err
	}
	name := filepath.Join(s.Directory, digest+".partial")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return "", errors.New("artifact transfer already running")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return "", errors.New("invalid artifact cache")
	}
	offset := stat.Size()
	if offset > MaxArchiveBytes || (expected > 0 && offset > expected) {
		if err = f.Truncate(0); err != nil {
			return "", err
		}
		offset = 0
	}
	verify := func() bool {
		if _, err := f.Seek(0, 0); err != nil {
			return false
		}
		h := sha256.New()
		_, err := io.Copy(h, f)
		return err == nil && hex.EncodeToString(h.Sum(nil)) == digest
	}
	if offset > 0 && verify() {
		if s.Resume != nil {
			s.Resume(offset, "verified_cache")
		}
		if s.Progress != nil {
			s.Progress("verified", offset, offset)
		}
		return name, nil
	}
	if expected > 0 && offset == expected {
		f.Truncate(0)
		offset = 0
	}
	endpoint := s.Origin + "/agent/v1/artifacts/" + digest
	if s.DeliveryID != "" {
		if !ops.Identifier.MatchString(s.DeliveryID) {
			return "", errors.New("invalid delivery ID")
		}
		endpoint = s.Origin + "/agent/v1/image-deliveries/" + s.DeliveryID + "/archive"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Host-ID", s.HostID)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := *s.Client
	client.Timeout = 0
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", ops.Fail("NETWORK_TIMEOUT")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 416 && offset > 0 {
		resp.Body.Close()
		if err = f.Truncate(0); err != nil {
			return "", err
		}
		offset = 0
		if s.Resume != nil {
			s.Resume(0, "range_rejected")
		}
		req.Header.Del("Range")
		resp, err = client.Do(req)
		if err != nil {
			return "", ops.Fail("NETWORK_TIMEOUT")
		}
		defer resp.Body.Close()
	}
	total := resp.ContentLength
	if resp.StatusCode == 206 {
		var start, end int64
		var trailing string
		n, _ := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d%s", &start, &end, &total, &trailing)
		if n != 3 || start != offset || end < start || end != total-1 || resp.ContentLength != end-start+1 {
			return "", errors.New("invalid artifact range")
		}
	} else if resp.StatusCode == 200 {
		if offset > 0 {
			if s.Resume != nil {
				s.Resume(0, "range_not_supported")
			}
			if err = f.Truncate(0); err != nil {
				return "", err
			}
			offset = 0
		}
	} else {
		if resp.StatusCode == 429 {
			return "", ops.Fail("RATE_LIMITED")
		}
		if resp.StatusCode >= 500 {
			return "", ops.Fail("REGISTRY_TEMPORARILY_UNAVAILABLE")
		}
		return "", errors.New("artifact unavailable or authorization expired")
	}
	if s.Resume != nil && offset > 0 {
		s.Resume(offset, "range_resumed")
	}
	if total <= 0 || total > MaxArchiveBytes || (expected > 0 && total != expected) {
		return "", errors.New("artifact size mismatch")
	}
	if err = CheckArtifactCapacity(s.Directory, total-offset); err != nil {
		return "", err
	}
	if err = RequireFreeSpace(s.Directory, uint64(total-offset)+(1<<30)); err != nil {
		return "", err
	}
	if _, err = f.Seek(offset, 0); err != nil {
		return "", err
	}
	if s.Progress != nil {
		s.Progress("downloading", offset, total)
	}
	writer := &artifactProgressWriter{w: f, current: offset, total: total, progress: s.Progress, transferred: s.Transferred}
	copied, err := io.Copy(writer, io.LimitReader(resp.Body, total-offset+1))
	syncErr := f.Sync()
	if err != nil || copied != total-offset || syncErr != nil {
		return "", ops.Fail("NETWORK_TIMEOUT")
	}
	if !verify() {
		f.Truncate(0)
		f.Sync()
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if s.Progress != nil {
		s.Progress("verified", total, total)
	}
	return name, nil
}

type artifactProgressWriter struct {
	transferred    func(int64)
	w              io.Writer
	current, total int64
	progress       func(string, int64, int64)
}

func (w *artifactProgressWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 && w.transferred != nil {
		w.transferred(int64(n))
	}
	w.current += int64(n)
	if w.progress != nil {
		w.progress("downloading", w.current, w.total)
	}
	return n, err
}

func VerifyImage(ctx context.Context, run Runner, imageID, platform string) error {
	raw, err := run(ctx, "image", "inspect", "--format", "{{.Id}} {{.Os}}/{{.Architecture}}", imageID)
	if err != nil {
		return errors.New("image not present")
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[0] != imageID {
		return ops.Fail("DIGEST_MISMATCH")
	}
	if fields[1] != platform {
		return ops.Fail("ARCH_MISMATCH")
	}
	return nil
}

// CanonicalContentLength is shared by the publisher's transfer checks.
func CanonicalContentLength(size int64) string { return strconv.FormatInt(size, 10) }

// Docker's classic store identifies an image by config digest; the containerd
// store may use the signed platform manifest digest. Both identities are bound
// by the trusted release; never accept an arbitrary ID from Docker output.
func VerifyReleaseImage(ctx context.Context, run Runner, image ops.ReleaseImage) (string, error) {
	if err := image.Validate(); err != nil {
		return "", err
	}
	candidates := []string{image.ImageID, strings.Split(image.Reference, "@")[1]}
	for _, id := range candidates {
		if VerifyImage(ctx, run, id, image.Platform) == nil {
			return id, nil
		}
	}
	return "", ops.Fail("DIGEST_MISMATCH")
}
func ImportReleaseArchive(ctx context.Context, run Runner, name string, image ops.ReleaseImage) (string, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = ValidateArchiveImage(f, image); err != nil {
		return "", err
	}
	expanded, err := inspectDockerArchive(f)
	if err != nil {
		return "", err
	}
	raw, err := run(ctx, "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(raw))
	if !filepath.IsAbs(root) {
		return "", errors.New("invalid Docker storage path")
	}
	if err = RequireFreeSpace(root, uint64(expanded)*2+(1<<30)); err != nil {
		return "", err
	}
	if _, err = run(ctx, "load", "--input", name); err != nil {
		return "", err
	}
	id, err := VerifyReleaseImage(ctx, run, image)
	if err == nil {
		return id, nil
	}
	resolved, err := ArchiveRuntimeImage(f, image)
	if err != nil {
		return "", err
	}
	if err = VerifyImage(ctx, run, resolved, image.Platform); err != nil {
		return "", err
	}
	return resolved, nil
}
