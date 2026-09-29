// Package settings handles local secret files without exposing their contents.
package settings

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func Secret(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("secret file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return "", errors.New("secret file unavailable or permissions exceed 0600")
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(b) > 8192 {
		return "", errors.New("secret file unavailable")
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", errors.New("empty secret file")
	}
	return s, nil
}
func Atomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
