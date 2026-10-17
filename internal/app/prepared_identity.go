package app

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/imagecontract"
	"github.com/alpha2z/skygo-admin/internal/transfer"
)

// preparedImageIdentity accepts the classic config ID or the platform manifest
// proven by the exact signed task archive. An arbitrary runtime ID is never trusted.
func (s *Server) preparedImageIdentity(c control.Command, runtimeID string) bool {
	if c.Archive == nil || !validRuntimeImageID(runtimeID) {
		return false
	}
	if runtimeID == c.Archive.ImageID {
		return true
	}
	if s.distribution == nil || !control.ImageIdentity("sha256:"+c.Archive.SHA256) || c.Archive.Size <= 0 || c.Archive.Size > 8<<30 {
		return false
	}
	file, err := os.OpenFile(filepath.Join(s.distribution.Directory, c.Archive.SHA256), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != c.Archive.Size {
		return false
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, c.Archive.Size+1))
	if err != nil || n != c.Archive.Size || hex.EncodeToString(hash.Sum(nil)) != c.Archive.SHA256 {
		return false
	}
	expected, err := transfer.ArchiveRuntimeImage(file, imagecontract.ReleaseImage{Role: c.Service, Reference: c.Image, Platform: c.Platform, ImageID: c.Archive.ImageID})
	return err == nil && expected == runtimeID
}
