package transfer

import (
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"io/fs"
	"path/filepath"
)

// Account for partial files and Admin blob subdirectories as well as completed
// archives. Never follow symlinks or prune files to make a request fit.
func CheckArtifactCapacity(directory string, incoming int64) error {
	return CheckCapacityLimit(directory, incoming, 64<<30)
}
func CheckCapacityLimit(directory string, incoming, limit int64) error {
	if limit == 0 {
		limit = 64 << 30
	}
	if incoming < 0 || incoming > limit {
		return ops.Fail("INSUFFICIENT_DISK")
	}
	var used int64
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return ops.Fail("INSUFFICIENT_DISK")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if info.Size() > limit-incoming-used {
				return ops.Fail("INSUFFICIENT_DISK")
			}
			used += info.Size()
		}
		return nil
	})
}
