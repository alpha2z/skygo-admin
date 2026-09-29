package control

import "errors"

// Targets are in start order; stopping always uses the reverse order.
// Unselected companions are pinned to their observed immutable image ID.
type UnitPlan struct {
	ID      string       `json:"id"`
	BootID  string       `json:"boot_id"`
	Targets []UnitTarget `json:"targets"`
}
type UnitTarget struct {
	Service         string `json:"service"`
	Kind            string `json:"kind"`
	Platform        string `json:"platform"`
	PreviousImageID string `json:"previous_image_id"`
	Image           string `json:"image"`
	ImageID         string `json:"image_id"`
	Revision        string `json:"revision"`
	Selected        bool   `json:"selected"`
}
type UnitProof struct {
	Service string `json:"service"`
	ImageID string `json:"image_id"`
	Healthy bool   `json:"healthy"`
}

func ImageIdentity(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	for _, r := range s[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func (p UnitPlan) Validate(root string) error {
	if !Identifier.MatchString(p.ID) || !Identifier.MatchString(p.BootID) || len(p.Targets) != 2 {
		return errors.New("invalid recovery unit")
	}
	roles, services := map[string]bool{}, map[string]bool{}
	selected := false
	for _, t := range p.Targets {
		if !Identifier.MatchString(t.Service) || services[t.Service] || roles[t.Kind] || (t.Kind != "admin-api" && t.Kind != "admin-web") || (t.Platform != "linux/amd64" && t.Platform != "linux/arm64") || !ImageIdentity(t.PreviousImageID) || !ImageIdentity(t.ImageID) || len(t.Revision) != 64 {
			return errors.New("invalid unit target")
		}
		if t.Selected {
			if !Image.MatchString(t.Image) || t.ImageID == t.PreviousImageID {
				return errors.New("invalid selected image")
			}
			selected = true
		} else if t.Image != t.PreviousImageID || t.ImageID != t.PreviousImageID {
			return errors.New("companion image changed")
		}
		roles[t.Kind] = true
		services[t.Service] = true
	}
	if !selected || !services[root] {
		return errors.New("empty unit change")
	}
	return nil
}
