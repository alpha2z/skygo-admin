package plugin

import (
	"bytes"
	"io/fs"
	"path"
	"time"
)

// AssetFS holds verified assets in memory; serving never reopens mutable paths.
type AssetFS map[string][]byte

func (a AssetFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	b, ok := a[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &assetFile{Reader: bytes.NewReader(b), info: assetInfo{name: path.Base(name), size: int64(len(b))}}, nil
}

type assetFile struct {
	*bytes.Reader
	info assetInfo
}

func (f *assetFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *assetFile) Close() error               { return nil }

type assetInfo struct {
	name string
	size int64
}

func (i assetInfo) Name() string       { return i.name }
func (i assetInfo) Size() int64        { return i.size }
func (i assetInfo) Mode() fs.FileMode  { return 0444 }
func (i assetInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (i assetInfo) IsDir() bool        { return false }
func (i assetInfo) Sys() any           { return nil }
