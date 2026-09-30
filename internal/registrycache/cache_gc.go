package registrycache

import (
	"encoding/json"
	"errors"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type DownloadCache struct {
	Image ops.ReleaseImage `json:"image"`
	Blobs []string         `json:"blobs"`
}

func writeDownloadCache(work string, image ops.ReleaseImage, layers []descriptor) error {
	record := DownloadCache{Image: image}
	for _, layer := range layers {
		record.Blobs = append(record.Blobs, layer.Digest)
	}
	raw, _ := json.Marshal(record)
	f, err := os.CreateTemp(work, ".cache-index-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
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
	return os.Rename(name, filepath.Join(work, "cache-index.json"))
}
func ReadDownloadCache(directory, key string) (DownloadCache, error) {
	var record DownloadCache
	if !validDigest("sha256:" + key) {
		return record, errors.New("invalid cache identity")
	}
	info, err := os.Lstat(filepath.Join(directory, ".registry-"+key))
	if err != nil {
		return record, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return record, errors.New("unsafe cache directory")
	}
	f, err := os.OpenFile(filepath.Join(directory, ".registry-"+key, "cache-index.json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return record, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return record, errors.New("invalid cache index")
	}
	if json.NewDecoder(f).Decode(&record) != nil || record.Image.Validate() != nil || record.Image.Key() != key || len(record.Blobs) > 256 {
		return record, errors.New("cache index mismatch")
	}
	for _, id := range record.Blobs {
		if !validDigest(id) {
			return record, errors.New("invalid cached layer")
		}
	}
	return record, nil
}

// RemoveDownloadCache removes only indexed, unshared content. Old/unreadable
// indexes protect all layer files; their ownership cannot be inferred safely.
func RemoveDownloadCache(directory string, image ops.ReleaseImage, inspect bool) (int64, error) {
	if image.Validate() != nil || !filepath.IsAbs(directory) {
		return 0, errors.New("invalid cache identity")
	}
	key := image.Key()
	record, err := ReadDownloadCache(directory, key)
	if err != nil {
		return 0, err
	}
	work := filepath.Join(directory, ".registry-"+key)
	lock, err := os.OpenFile(filepath.Join(work, "lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return 0, errors.New("download cache in use")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	references := map[string]bool{}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	unknown := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".registry-") || entry.Name() == ".registry-"+key || !entry.IsDir() {
			continue
		}
		other, e := ReadDownloadCache(directory, strings.TrimPrefix(entry.Name(), ".registry-"))
		if e != nil {
			unknown = true
			continue
		}
		for _, blob := range other.Blobs {
			references[blob] = true
		}
	}
	var size int64
	err = filepath.WalkDir(work, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe cache entry")
		}
		if !entry.IsDir() {
			info, e := entry.Info()
			if e != nil {
				return e
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	remove := []string{}
	if !unknown {
		for _, id := range record.Blobs {
			if references[id] {
				continue
			}
			for _, suffix := range []string{"", ".partial"} {
				path := filepath.Join(directory, "registry-blobs", id[7:]+suffix)
				info, e := os.Lstat(path)
				if os.IsNotExist(e) {
					continue
				}
				if e != nil || !info.Mode().IsRegular() {
					return 0, errors.New("unsafe layer file")
				}
				size += info.Size()
				remove = append(remove, path)
			}
		}
	}
	if inspect {
		return size, nil
	}
	for _, path := range remove {
		if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
			return 0, e
		}
	}
	return size, os.RemoveAll(work)
}
