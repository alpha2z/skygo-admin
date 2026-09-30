package registrycache

import (
	"errors"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"strings"
	"time"
)

// The labels are read only after the config digest is checked against the signed
// image identity. Historical images without these labels retain their old form.
func readableImageTag(image ops.ReleaseImage, labels map[string]string) (string, error) {
	version := labels["io.skygo-admin.build.version"]
	if version == "" && labels["io.skygo-admin.build.timezone"] == "" {
		return "", nil
	}
	created, err := time.Parse(time.RFC3339Nano, labels["org.opencontainers.image.created"])
	if err != nil || created.Year() < 2020 || created.Year() > 2100 || labels["io.skygo-admin.build.timezone"] != "Asia/Tokyo" || image.Validate() != nil {
		return "", errors.New("invalid Tokyo build labels")
	}
	// Asia/Tokyo is UTC+9 throughout the accepted modern build-date range.
	expected := created.In(time.FixedZone("JST", 9*60*60)).Format("20060102-150405") + "-" + strings.TrimPrefix(image.Platform, "linux/")
	if version != expected {
		return "", errors.New("Tokyo build timestamp mismatch")
	}
	return "skygo-admin/" + image.Role + ":" + version, nil
}
