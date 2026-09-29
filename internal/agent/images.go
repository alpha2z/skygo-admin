package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"regexp"
	"strings"
)

type imageDetails struct {
	ID           string `json:"Id"`
	OS           string `json:"Os"`
	Architecture string
	RepoDigests  []string
}

func inspectImage(ctx context.Context, reference string) (imageDetails, error) {
	var image imageDetails
	raw, err := run(ctx, os.Environ(), "image", "inspect", "--format", "{{json .}}", reference)
	if err != nil || json.Unmarshal(raw, &image) != nil || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image.ID) {
		return image, errors.New("image inspection failed")
	}
	return image, nil
}
func (image imageDetails) platform() string { return image.OS + "/" + image.Architecture }
func (image imageDetails) matches(reference, platform string) bool {
	if image.platform() != platform {
		return false
	}
	for _, digest := range image.RepoDigests {
		if digest == reference {
			return true
		}
	}
	return false
}

// prepareImage only populates and verifies Docker's image cache. In particular,
// it must never invoke Compose, create a container or change a configuration.
func prepareImage(ctx context.Context, s LocalService, c control.Command) control.Result {
	r := control.Result{ID: c.ID, Status: "failed", Code: "IMAGE_NOT_APPROVED"}
	allowed := false
	for _, repository := range s.AllowedImages {
		if repository == strings.Split(c.Image, "@")[0] {
			allowed = true
		}
	}
	if !allowed || !control.Image.MatchString(c.Image) || (c.Platform != "linux/amd64" && c.Platform != "linux/arm64") {
		return r
	}
	image, err := inspectImage(ctx, c.Image)
	if err != nil {
		if _, err = run(ctx, os.Environ(), "pull", "--platform", c.Platform, c.Image); err != nil {
			r.Code = "IMAGE_PULL_FAILED"
			return r
		}
		image, err = inspectImage(ctx, c.Image)
	}
	if err != nil {
		r.Code = "IMAGE_INSPECT_FAILED"
		return r
	}
	if !image.matches(c.Image, c.Platform) {
		r.Code = "IMAGE_IDENTITY_MISMATCH"
		return r
	}
	r.Status = "succeeded"
	r.Code = "IMAGE_PREPARED"
	r.Image = c.Image
	r.ImageID = image.ID
	r.Platform = image.platform()
	return r
}
