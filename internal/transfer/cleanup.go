package transfer

import (
	"context"
	"encoding/json"
	"errors"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ImageCleanup only removes content specifically admitted by a signed request.
// It is called by the serialized Agent worker, never concurrently with deployment.
func ImageCleanup(ctx context.Context, p ops.ImageCleanup, directory string, run Runner) (ops.CleanupResult, error) {
	result := ops.CleanupResult{Status: "protected"}
	if p.Validate() != nil || !filepath.IsAbs(directory) {
		return result, errors.New("invalid cleanup request")
	}
	lock, err := os.OpenFile(filepath.Join(directory, ".distribution.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return result, errors.New("image synchronization in progress")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	runtimeID, verifyErr := VerifyReleaseImage(ctx, run, p.Image)
	if verifyErr == nil {
		result.RuntimeImageID = runtimeID
		// Includes stopped containers. Never use --force or prune to evade a reference.
		raw, err := run(ctx, "ps", "-aq", "--filter", "ancestor="+runtimeID)
		if err != nil {
			return result, err
		}
		if strings.TrimSpace(string(raw)) != "" {
			result.Reason = "Image is referenced by a running or stopped container"
			return result, nil
		}
	} else if p.Kind == "image" {
		// A failed identity verification must not be treated as proof of absence.
		raw, err := run(ctx, "image", "ls", "--no-trunc", "--quiet")
		if err != nil {
			return result, err
		}
		if strings.Contains(string(raw), p.Image.ImageID) {
			result.Reason = "Unable to verify image identity"
			return result, nil
		}
		result.Status = "absent"
		return result, nil
	}
	if p.Kind == "archive" {
		name := filepath.Join(directory, p.ArchiveSHA256+".partial")
		file, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if os.IsNotExist(err) {
			result.Status = "absent"
			return result, nil
		}
		if err != nil {
			return result, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return result, errors.New("invalid cleanup file")
		}
		if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return result, errors.New("archive in use")
		}
		defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		result.Size = info.Size()
		result.Status = "ready"
		if p.Inspect {
			return result, nil
		}
		if err = os.Remove(name); err != nil {
			return result, err
		}
		result.Status = "deleted"
		// Filesystem sharing/compression can differ from logical file size.
		result.Reclaimed = nil
		return result, nil
	}
	raw, err := run(ctx, "image", "inspect", runtimeID)
	if err != nil {
		return result, err
	}
	var images []struct{ Size int64 }
	if json.Unmarshal(raw, &images) != nil || len(images) != 1 {
		return result, errors.New("image metadata unavailable")
	}
	result.Size = images[0].Size
	result.Status = "ready"
	if p.Inspect {
		return result, nil
	}
	if _, err = run(ctx, "image", "rm", "--no-prune", runtimeID); err != nil {
		result.Status = "protected"
		result.Reason = "Image is still referenced or Docker refused deletion"
		return result, nil
	}
	result.Status = "deleted" // Shared layers make exact reclaimed bytes unavailable.
	return result, nil
}
