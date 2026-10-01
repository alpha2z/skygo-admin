// Package control defines the versioned, business-independent agent contract.
package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const Version = 1

var Identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var Image = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

// Paths, environment values and commands are deliberately absent. They belong
// in the operator-owned agent inventory and cannot be supplied over the API.
type Service struct {
	ID           string   `json:"id"`
	HostID       string   `json:"host_id"`
	Image        string   `json:"image"`
	DependsOn    []string `json:"depends_on"`
	ControlKind  string   `json:"control_kind,omitempty"`
	Platform     string   `json:"platform,omitempty"`
	ControlPlane bool     `json:"control_plane"`
}
type Command struct {
	WorkflowScopeRevision string          `json:"workflow_scope_revision,omitempty"`
	WorkflowImage         string          `json:"workflow_image,omitempty"`
	WorkflowID            string          `json:"workflow_id,omitempty"`
	Extension             string          `json:"extension,omitempty"`
	Payload               json.RawMessage `json:"payload,omitempty"`
	SyncImageEnv          bool            `json:"sync_image_env,omitempty"`
	InventoryRevision     string          `json:"inventory_revision,omitempty"`
	PreviousImageID       string          `json:"previous_image_id,omitempty"`
	Cleanup               *Cleanup        `json:"cleanup,omitempty"`
	Archive               *Archive        `json:"archive,omitempty"`
	DeliveryID            string          `json:"delivery_id,omitempty"`
	Unit                  *UnitPlan       `json:"unit,omitempty"`
	Platform              string          `json:"platform,omitempty"`
	Version               int             `json:"version"`
	ID                    string          `json:"id"`
	HostID                string          `json:"host_id"`
	Service               string          `json:"service"`
	Action                string          `json:"action"`
	Image                 string          `json:"image,omitempty"`
	Config                json.RawMessage `json:"config,omitempty"`
	ConfigHash            string          `json:"config_hash,omitempty"`
	ExpiresAt             time.Time       `json:"expires_at"`
}
type Envelope struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
}
type Result struct {
	Data       json.RawMessage `json:"data,omitempty"`
	Unit       []UnitProof     `json:"unit,omitempty"`
	ImageID    string          `json:"image_id,omitempty"`
	Platform   string          `json:"platform,omitempty"`
	Logs       string          `json:"logs,omitempty"`
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Code       string          `json:"code"`
	Image      string          `json:"image,omitempty"`
	Healthy    bool            `json:"healthy"`
	ConfigHash string          `json:"config_hash,omitempty"`
}
type Observation struct {
	ScopeRevision     string                     `json:"scope_revision,omitempty"`
	ConfiguredImage   string                     `json:"configured_image,omitempty"`
	Extensions        map[string]json.RawMessage `json:"extensions,omitempty"`
	SyncImageEnv      bool                       `json:"sync_image_env,omitempty"`
	Running           bool                       `json:"running"`
	UnitID            string                     `json:"unit_id,omitempty"`
	ControlKind       string                     `json:"control_kind,omitempty"`
	InventoryRevision string                     `json:"inventory_revision,omitempty"`
	ImageID           string                     `json:"image_id,omitempty"`
	Platform          string                     `json:"platform,omitempty"`
	Capabilities      []string                   `json:"capabilities,omitempty"`
	Service           string                     `json:"service"`
	Image             string                     `json:"image"`
	Healthy           bool                       `json:"healthy"`
	ConfigHash        string                     `json:"config_hash,omitempty"`
}
type Heartbeat struct {
	Version      int           `json:"version"`
	BootID       string        `json:"boot_id"`
	Observations []Observation `json:"observations"`
}

func Digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func (c Command) Validate() error {
	if (c.WorkflowID != "" && !Identifier.MatchString(c.WorkflowID)) || c.Version != Version || !Identifier.MatchString(c.ID) || !Identifier.MatchString(c.HostID) || !Identifier.MatchString(c.Service) || c.ExpiresAt.IsZero() {
		return errors.New("invalid command identity")
	}
	if c.WorkflowScopeRevision != "" && (c.WorkflowID == "" || len(c.WorkflowScopeRevision) != 64 || !Image.MatchString(c.WorkflowImage) || !ImageIdentity(c.PreviousImageID)) {
		return errors.New("invalid workflow local scope")
	}
	if c.SyncImageEnv && ((c.Action != "deploy" && c.Action != "rollback") || len(c.InventoryRevision) != 64 || !ImageIdentity(c.PreviousImageID)) {
		return errors.New("invalid image environment scope")
	}
	if c.Cleanup != nil && c.Action != "image-cleanup" {
		return errors.New("unexpected cleanup scope")
	}
	if c.Archive != nil && (c.Action != "prepare-image" || !ImageIdentity("sha256:"+c.Archive.SHA256) || !ImageIdentity(c.Archive.ImageID) || c.Archive.Size <= 0 || c.Archive.Size > 8<<30) {
		return errors.New("invalid archive attachment")
	}
	if c.DeliveryID != "" && (c.Action != "prepare-image" || !Identifier.MatchString(c.DeliveryID)) {
		return errors.New("invalid delivery identity")
	}
	if c.Action != "prepare-image" && c.Platform != "" {
		return errors.New("unexpected platform")
	}
	if c.Action != "control-unit" && c.Unit != nil {
		return errors.New("unexpected unit")
	}
	if c.Action != "extension" && (c.Extension != "" || len(c.Payload) > 0) {
		return errors.New("unexpected extension payload")
	}
	switch c.Action {
	case "extension":
		if !Identifier.MatchString(c.Extension) || len(c.Payload) == 0 || len(c.Payload) > 256<<10 || !json.Valid(c.Payload) || c.Image != "" || len(c.Config) > 0 || c.ConfigHash != "" {
			return errors.New("invalid extension command")
		}
	case "image-cleanup":
		if c.Cleanup == nil || !Image.MatchString(c.Image) || len(c.Config) > 0 || !ImageIdentity(c.Cleanup.ImageID) || (c.Cleanup.Platform != "linux/amd64" && c.Cleanup.Platform != "linux/arm64") || (c.Cleanup.Kind != "image" && c.Cleanup.Kind != "archive") || (c.Cleanup.Kind == "archive" && !ImageIdentity("sha256:"+c.Cleanup.SHA256)) {
			return errors.New("invalid cleanup scope")
		}
	case "control-unit":
		if c.Image != "" || len(c.Config) > 0 || c.ConfigHash != "" || c.Unit == nil {
			return errors.New("invalid unit payload")
		}
		return c.Unit.Validate(c.Service)
	case "start", "stop", "restart", "health", "logs":
		if c.Image != "" || len(c.Config) > 0 {
			return errors.New("unexpected action payload")
		}
	case "prepare-image":
		if !Image.MatchString(c.Image) || len(c.Config) > 0 || (c.Platform != "linux/amd64" && c.Platform != "linux/arm64") {
			return errors.New("invalid image preparation")
		}
	case "deploy", "rollback":
		if !Image.MatchString(c.Image) || len(c.Config) > 0 {
			return errors.New("immutable image required")
		}
	case "configure":
		canonical, _ := json.Marshal(c.Config)
		if !bytes.Equal(canonical, c.Config) {
			return errors.New("configuration must use canonical compact JSON")
		}
		if c.Image != "" || len(c.Config) == 0 || len(c.Config) > 256<<10 || !json.Valid(c.Config) || Digest(c.Config) != c.ConfigHash {
			return errors.New("invalid configuration")
		}
	default:
		return errors.New("unsupported action")
	}
	return nil
}
func Sign(c Command, k ed25519.PrivateKey) (Envelope, error) {
	if err := c.Validate(); err != nil {
		return Envelope{}, err
	}
	if len(k) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("invalid signing key")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{b, ed25519.Sign(k, b)}, nil
}
func Verify(e Envelope, k ed25519.PublicKey, host string) (Command, error) {
	var c Command
	if len(k) != ed25519.PublicKeySize || len(e.Payload) > 512<<10 || !ed25519.Verify(k, e.Payload, e.Signature) {
		return c, errors.New("signature rejected")
	}
	if json.Unmarshal(e.Payload, &c) != nil {
		return c, errors.New("invalid payload")
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if c.HostID != host {
		return c, errors.New("wrong host")
	}
	return c, nil
}
func ValidateServices(services []Service) error {
	all := map[string]Service{}
	for _, s := range services {
		if s.ControlKind != "" && (!s.ControlPlane || (s.ControlKind != "admin-api" && s.ControlKind != "admin-web" && s.ControlKind != "ops-agent") || (s.Platform != "linux/amd64" && s.Platform != "linux/arm64")) {
			return errors.New("invalid control service metadata")
		}
		if !Identifier.MatchString(s.ID) || !Identifier.MatchString(s.HostID) || !Image.MatchString(s.Image) {
			return errors.New("invalid service")
		}
		if _, ok := all[s.ID]; ok {
			return errors.New("duplicate service")
		}
		all[s.ID] = s
	}
	seen, active := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return errors.New("dependency cycle")
		}
		if seen[id] {
			return nil
		}
		s, ok := all[id]
		if !ok {
			return errors.New("missing dependency")
		}
		active[id] = true
		for _, d := range s.DependsOn {
			if err := visit(d); err != nil {
				return err
			}
		}
		active[id] = false
		seen[id] = true
		return nil
	}
	for id := range all {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
