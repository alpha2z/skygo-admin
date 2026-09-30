// Package imagecontract contains immutable image identities and safe error codes.
package imagecontract

import (
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
)

type ReleaseImage struct {
	Role      string `json:"service"`
	Platform  string `json:"platform"`
	Reference string `json:"reference"`
	ImageID   string `json:"image_id,omitempty"`
}

func (i ReleaseImage) Validate() error {
	if !control.Identifier.MatchString(i.Role) || !control.Image.MatchString(i.Reference) || (i.Platform != "linux/amd64" && i.Platform != "linux/arm64") || (i.ImageID != "" && !control.ImageIdentity(i.ImageID)) {
		return errors.New("invalid image identity")
	}
	return nil
}
func (i ReleaseImage) Key() string {
	b, _ := json.Marshal([]string{i.Role, i.Platform, i.Reference})
	return control.Digest(b)
}

type Failure string

func (f Failure) Error() string { return string(f) }
func Fail(code string) error    { return Failure(code) }
func FailureCode(err error) string {
	var f Failure
	if errors.As(err, &f) {
		return string(f)
	}
	return "OPERATION_FAILED"
}

var Identifier = control.Identifier

type ImageCleanup struct {
	Kind          string       `json:"kind"`
	Image         ReleaseImage `json:"image"`
	ArchiveSHA256 string       `json:"archive_sha256,omitempty"`
	Inspect       bool         `json:"inspect"`
}

func (c ImageCleanup) Validate() error {
	if c.Image.Validate() != nil || !control.ImageIdentity(c.Image.ImageID) || (c.Kind != "image" && c.Kind != "archive") || (c.Kind == "archive" && !control.ImageIdentity("sha256:"+c.ArchiveSHA256)) {
		return errors.New("invalid cleanup scope")
	}
	return nil
}

type CleanupResult struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Size           int64  `json:"size"`
	Reclaimed      *int64 `json:"reclaimed,omitempty"`
	RuntimeImageID string `json:"runtime_image_id,omitempty"`
}
