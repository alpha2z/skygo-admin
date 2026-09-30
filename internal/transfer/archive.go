package transfer

import (
	"archive/tar"
	"context"
	"errors"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"io"
	"net/http"
	"path"
	"strings"
	"syscall"
)

const MaxArchiveBytes int64 = 8 << 30
const MaxExpandedBytes int64 = 64 << 30

type Runner func(context.Context, ...string) ([]byte, error)

func ValidateDockerArchive(reader io.Reader) error {
	_, err := inspectDockerArchive(reader)
	return err
}
func inspectDockerArchive(reader io.Reader) (int64, error) {
	archive := tar.NewReader(reader)
	var total int64
	count := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, errors.New("invalid Docker archive")
		}
		count++
		clean := path.Clean(header.Name)
		if path.IsAbs(header.Name) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(header.Name, "\\") || count > 100000 {
			return 0, errors.New("unsafe Docker archive path or entry count")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return 0, errors.New("archive links and special entries are forbidden")
		}
		if header.Size < 0 || header.Size > MaxExpandedBytes-total {
			return 0, errors.New("expanded Docker archive too large")
		}
		total += header.Size
	}
	if count == 0 {
		return 0, errors.New("empty Docker archive")
	}
	return total, nil
}

type ImageSource struct {
	Transferred func(int64)
	Resume      func(int64, string)
	DeliveryID  string
	Progress    func(string, int64, int64)

	Directory, Origin, HostID string
	Client                    *http.Client
}

func RequireFreeSpace(directory string, required uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(directory, &stat); err != nil {
		return errors.New("cannot verify local disk space")
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if available < required {
		return ops.Fail("INSUFFICIENT_DISK")
	}
	return nil
}
