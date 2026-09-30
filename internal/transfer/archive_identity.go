package transfer

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	ops "github.com/alpha2z/skygo-admin/internal/imagecontract"
	"io"
	"strings"
)

// ValidateArchiveImage checks the export's config identity and platform without
// asking the API host to import or execute it. The registry manifest is bound by
// the trusted source release and checked by the mirror's digest pull.
func ValidateArchiveImage(f io.ReadSeeker, image ops.ReleaseImage) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := ValidateDockerArchive(f); err != nil {
		return err
	}
	f.Seek(0, 0)
	tarReader := tar.NewReader(f)
	configs := map[string]bool{}
	var manifest []struct {
		Config string `json:"Config"`
	}
	foundManifest := false
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Name == "manifest.json" {
			if foundManifest || header.Size > 1<<20 {
				return errors.New("invalid image manifest")
			}
			foundManifest = true
			if json.NewDecoder(io.LimitReader(tarReader, 1<<20)).Decode(&manifest) != nil {
				return errors.New("invalid image manifest")
			}
			continue
		}
		if header.Size > 2<<20 {
			continue
		}
		raw, err := io.ReadAll(tarReader)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if "sha256:"+hex.EncodeToString(sum[:]) != image.ImageID {
			continue
		}
		var config struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		}
		if json.Unmarshal(raw, &config) != nil || config.OS+"/"+config.Architecture != image.Platform {
			return ops.Fail("ARCH_MISMATCH")
		}
		configs[header.Name] = true
	}
	if !foundManifest || len(manifest) != 1 || !configs[manifest[0].Config] {
		return ops.Fail("DIGEST_MISMATCH")
	}
	_, err := f.Seek(0, 0)
	return err
}

// ArchiveRuntimeImage follows the signed Registry root through an optional
// platform index. It only accepts a manifest linked to the signed config ID.
func ArchiveRuntimeImage(f io.ReadSeeker, image ops.ReleaseImage) (string, error) {
	if image.Validate() != nil {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", err
	}
	type descriptor struct {
		Digest   string                            `json:"digest"`
		Platform struct{ OS, Architecture string } `json:"platform"`
	}
	type manifest struct {
		SchemaVersion int          `json:"schemaVersion"`
		Config        descriptor   `json:"config"`
		Manifests     []descriptor `json:"manifests"`
	}
	manifests := map[string]manifest{}
	reader := tar.NewReader(f)
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Size > 2<<20 || header.Size <= 0 {
			continue
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			return "", err
		}
		var m manifest
		if json.Unmarshal(raw, &m) != nil || m.SchemaVersion != 2 || (m.Config.Digest != image.ImageID && len(m.Manifests) == 0) {
			continue
		}
		total += int64(len(raw))
		if total > 8<<20 {
			return "", ops.Fail("DIGEST_MISMATCH")
		}
		sum := sha256.Sum256(raw)
		manifests["sha256:"+hex.EncodeToString(sum[:])] = m
	}
	rootID := strings.Split(image.Reference, "@")[1]
	root, ok := manifests[rootID]
	if !ok {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	if len(root.Manifests) == 0 {
		if root.Config.Digest != image.ImageID {
			return "", ops.Fail("DIGEST_MISMATCH")
		}
		return rootID, nil
	}
	selected := ""
	for _, d := range root.Manifests {
		if d.Platform.OS+"/"+d.Platform.Architecture == image.Platform {
			if selected != "" {
				return "", ops.Fail("ARCH_MISMATCH")
			}
			selected = d.Digest
		}
	}
	selectedManifest, ok := manifests[selected]
	if !ok || selectedManifest.Config.Digest != image.ImageID || len(selectedManifest.Manifests) != 0 {
		return "", ops.Fail("DIGEST_MISMATCH")
	}
	return selected, nil
}
