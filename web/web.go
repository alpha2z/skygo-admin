// Package web exposes the canonical management frontend without copying it into
// downstream repositories. Private pages are registered through admin.Extension.
package web

import (
	assets "github.com/alpha2z/skygo-admin/admin-web"
	"io/fs"
)

var FS fs.FS = assets.FS
