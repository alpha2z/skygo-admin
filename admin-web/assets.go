// Package webassets embeds the canonical management frontend for composition builds.
package webassets

import "embed"

//go:embed index.html *.js *.css
var FS embed.FS
