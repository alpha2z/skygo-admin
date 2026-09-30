package registrycache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	opsagent "github.com/alpha2z/skygo-admin/internal/transfer"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func verifiedBlob(path, id string, size int64) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	return err == nil && "sha256:"+hex.EncodeToString(h.Sum(nil)) == id
}

// Partial blobs survive transport loss and Admin restarts. They are never used
// for import until both the exact length and the manifest digest are verified.
func (c *Client) blob(ctx context.Context, repository string, d descriptor, token, directory string, progress func(int64), resume ...func(int64, string)) (string, error) {
	if !validDigest(d.Digest) || d.Size <= 0 || d.Size > maxArchive {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	root := filepath.Join(directory, "registry-blobs")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(root, d.Digest[7:])
	if verifiedBlob(path, d.Digest, d.Size) {
		if len(resume) > 0 && resume[0] != nil {
			resume[0](d.Size, "verified_cache")
		}
		return path, nil
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return "", err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	offset := info.Size()
	if offset > d.Size {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if err := opsagent.CheckArtifactCapacity(directory, d.Size-offset); err != nil {
		return "", err
	}
	if offset < d.Size {
		resp, err := c.getOffset(ctx, repository, "blobs", d.Digest, token, offset)
		if err != nil {
			return "", err
		}
		if resp.StatusCode == 416 {
			resp.Body.Close()
			if err = f.Truncate(0); err != nil {
				return "", err
			}
			offset = 0
			if len(resume) > 0 && resume[0] != nil {
				resume[0](0, "range_rejected")
			}
			resp, err = c.getOffset(ctx, repository, "blobs", d.Digest, token, 0)
			if err != nil {
				return "", err
			}
		}
		defer resp.Body.Close()
		if resp.StatusCode == 200 && offset > 0 {
			if err = f.Truncate(0); err != nil {
				return "", err
			}
			offset = 0
			if len(resume) > 0 && resume[0] != nil {
				resume[0](0, "range_not_supported")
			}
		}
		if resp.StatusCode == 206 && resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, d.Size-1, d.Size) {
			return "", ops.Fail("DIGEST_MISMATCH")
		}
		if resp.ContentLength >= 0 && resp.ContentLength != d.Size-offset {
			return "", ops.Fail("DIGEST_MISMATCH")
		}
		if offset > 0 && len(resume) > 0 && resume[0] != nil {
			resume[0](offset, "range_resumed")
		}
		if _, err = f.Seek(offset, 0); err != nil {
			return "", err
		}
		n, copyErr := io.Copy(&blobProgressWriter{writer: &spaceWriter{writer: f, directory: directory, quota: c.MaxCacheBytes}, offset: offset, progress: progress}, io.LimitReader(resp.Body, d.Size-offset+1))
		syncErr := f.Sync()
		if progress != nil {
			progress(offset + n)
		}
		if n > d.Size-offset {
			f.Close()
			os.Remove(partial)
			return "", ops.Fail("DIGEST_MISMATCH")
		}
		if copyErr != nil && ops.FailureCode(copyErr) != "OPERATION_FAILED" {
			return "", copyErr
		}
		if copyErr != nil || n != d.Size-offset {
			return "", ops.Fail("NETWORK_TIMEOUT")
		}
		if syncErr != nil {
			return "", syncErr
		}
	}
	if !verifiedBlob(partial, d.Digest, d.Size) {
		f.Close()
		os.Remove(partial)
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(partial, path); err != nil {
		return "", err
	}
	return path, nil
}

type blobProgressWriter struct {
	writer   io.Writer
	offset   int64
	last     time.Time
	progress func(int64)
}

func (w *blobProgressWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.offset += int64(n)
	if w.progress != nil && time.Since(w.last) >= 2*time.Second {
		w.last = time.Now()
		w.progress(w.offset)
	}
	return n, err
}
