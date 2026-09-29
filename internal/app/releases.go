package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/githubbuild"
	"github.com/alpha2z/skygo-admin/internal/release"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
	"time"
)

type githubBuildClient interface {
	Config() githubbuild.Config
	Runs(context.Context) ([]githubbuild.Run, error)
	ValidateDispatch(string, string, string) error
	Dispatch(context.Context, string, string, string) error
	SignedImages(context.Context, int64) (githubbuild.SignedArtifact, error)
}
type BuildTrustKey struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	PublicKey string    `gorm:"size:64" json:"public_key"`
	CreatedAt time.Time `json:"created_at"`
}
type ImageRelease struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	Payload   string    `gorm:"type:mediumtext" json:"-"`
	CreatedAt time.Time `json:"created_at"`
}
type ImagePreparation struct {
	HostBootID string `gorm:"size:64"`
	ID         string `gorm:"primaryKey;size:64"`
	TaskID     string `gorm:"size:64"`
}
type releaseError struct{ Code, Message string }

func (e *releaseError) Error() string { return e.Code }
func releaseFailure(code string) *releaseError {
	messages := map[string]string{
		"BUILD_TRUST_MISSING": "Add a separate CI build public key in Release settings.", "BUILD_TRUST_UNAVAILABLE": "Build trust could not be read. Refresh before proceeding.", "BUILD_SIGNATURE_UNTRUSTED": "The current build public keys cannot verify this artifact.", "BUILD_PROVENANCE_MISMATCH": "Artifact repository, workflow, commit or attempt does not match the approved run.", "REGISTRATION_CONTENT_CONFLICT": "This immutable release ID already contains different content.", "REGISTRATION_STATUS_UNAVAILABLE": "Registration status is unknown; refresh before retrying.", "REGISTRATION_CONTENT_INVALID": "Stored release content requires investigation.", "ADMIN_PAIR_MISSING": "The release must contain API and Web images for the same architecture.", "ADMIN_TARGET_AMBIGUOUS": "This host contains multiple services for the same control role.", "ADMIN_TARGET_MISSING": "Register exactly one API and one Web control service on this host.", "ADMIN_HOST_OFFLINE": "The host is offline or its heartbeat is stale.", "ADMIN_CAPABILITY_MISSING": "Upgrade the agent and wait for image.prepare.v1 inventory.", "ADMIN_PLATFORM_MISMATCH": "Service and reported container architectures do not match.", "ADMIN_PREVIEW_CHANGED": "The preparation preview has changed. Refresh and review the target.", "IMAGE_PREPARE_PENDING": "A preparation is still in progress.", "GITHUB_RUN_NOT_APPROVED": "The run is not an approved successful workflow run.", "GITHUB_SIGNED_ARTIFACT_MISSING": "This attempt has no signed image manifest.", "GITHUB_ARTIFACT_INVALID": "The build artifact failed format or identity validation.", "GITHUB_ARTIFACT_DIGEST_MISMATCH": "The downloaded artifact digest does not match GitHub metadata.", "GITHUB_ARTIFACT_REDIRECT_REJECTED": "The artifact download origin was rejected.", "GITHUB_ARTIFACT_DOWNLOAD_FAILED": "The signed artifact could not be downloaded.", "GITHUB_CREDENTIAL_UNAVAILABLE": "The provider credential file is unavailable or insufficiently protected.",
		"GITHUB_AUTH_FAILED":          "The provider rejected its configured credential.",
		"GITHUB_ACCESS_DENIED":        "Check Actions permissions for the approved repository.",
		"GITHUB_WORKFLOW_UNAVAILABLE": "The approved workflow is unavailable.",
		"GITHUB_CONNECTION_FAILED":    "Provider outcome is uncertain; inspect builds before dispatching again.",
		"GITHUB_RATE_LIMITED":         "The build provider is rate limited. Refresh later.",
		"GITHUB_UNAVAILABLE":          "The build provider is unavailable; check status before retrying."}
	message, ok := messages[code]
	if !ok {
		code = "GITHUB_UNAVAILABLE"
		message = messages[code]
	}
	return &releaseError{code, message}
}
func writeReleaseError(c *gin.Context, status int, code string) {
	e := releaseFailure(code)
	c.JSON(status, gin.H{"error": e.Message, "error_code": e.Code})
}
func safeBuildCode(err error) string {
	var e *githubbuild.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "GITHUB_UNAVAILABLE"
}
func (s *Server) buildKeys(tx *gorm.DB) ([]ed25519.PublicKey, error) {
	var rows []BuildTrustKey
	if err := tx.Order("id").Find(&rows).Error; err != nil {
		return nil, releaseFailure("BUILD_TRUST_UNAVAILABLE")
	}
	if len(rows) == 0 {
		return nil, releaseFailure("BUILD_TRUST_MISSING")
	}
	keys := []ed25519.PublicKey{}
	for _, row := range rows {
		k, err := base64.StdEncoding.DecodeString(row.PublicKey)
		if err != nil || len(k) != ed25519.PublicKeySize || control.Digest(k) != row.ID || bytes.Equal(k, s.cfg.SigningKey.Public().(ed25519.PublicKey)) {
			return nil, releaseFailure("BUILD_TRUST_UNAVAILABLE")
		}
		keys = append(keys, ed25519.PublicKey(k))
	}
	return keys, nil
}
func (s *Server) releaseSettings(c *gin.Context) {
	var keys []BuildTrustKey
	if s.db.Order("created_at").Find(&keys).Error != nil {
		writeReleaseError(c, 503, "BUILD_TRUST_UNAVAILABLE")
		return
	}
	manage, _ := s.auth.Enforce(c.GetString("admin_role"), "admin.manage")
	build, _ := s.auth.Enforce(c.GetString("admin_role"), "build.write")
	out := gin.H{"keys": keys, "can_add_key": manage && build, "github_configured": s.github != nil, "registry_mode": "agent-local", "key_count": len(keys)}
	if s.github != nil {
		out["github"] = s.github.Config()
		out["checked_at"] = time.Now().UTC()
		if _, err := s.github.Runs(c.Request.Context()); err != nil {
			out["github_connected"] = false
			out["github_error"] = releaseFailure(safeBuildCode(err)).Message
		} else {
			out["github_connected"] = true
		}
	}
	c.JSON(200, out)
}
func (s *Server) addBuildKey(c *gin.Context) {
	var req struct {
		PublicKey string `json:"public_key"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	key, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || bytes.Equal(key, s.cfg.SigningKey.Public().(ed25519.PublicKey)) {
		c.JSON(400, gin.H{"error": "Use a separate Ed25519 build public key."})
		return
	}
	id := control.Digest(key)
	s.change(c, body, "build.trust.add", id, func(tx *gorm.DB) (any, error) {
		var old BuildTrustKey
		err := tx.First(&old, "id = ?", id).Error
		if err == nil {
			return unchangedMutation{old}, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		var count int64
		if tx.Model(&BuildTrustKey{}).Count(&count).Error != nil || count >= 8 {
			return nil, errConflict
		}
		row := BuildTrustKey{ID: id, PublicKey: base64.StdEncoding.EncodeToString(key), CreatedAt: time.Now().UTC()}
		return row, tx.Create(&row).Error
	})
}

type registration struct {
	Status       string          `json:"status"`
	ReleaseID    string          `json:"release_id,omitempty"`
	RegisteredAt *time.Time      `json:"registered_at,omitempty"`
	Images       []release.Image `json:"images,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
}

func registrationID(run githubbuild.Run) string { return fmt.Sprintf("ci-%d-%d", run.ID, run.Attempt) }
func registrationFromRow(run githubbuild.Run, config githubbuild.Config, row ImageRelease, exists bool) registration {
	if run.Attempt < 1 {
		return registration{Status: "unknown", ErrorCode: "REGISTRATION_STATUS_UNAVAILABLE"}
	}
	if !exists {
		return registration{Status: "unregistered"}
	}
	var signed release.SignedManifest
	if json.Unmarshal([]byte(row.Payload), &signed) != nil {
		return registration{Status: "unknown", ErrorCode: "REGISTRATION_CONTENT_INVALID"}
	}
	manifest, err := release.Decode(signed)
	artifact := githubbuild.SignedArtifact{Run: run, Attempt: run.Attempt}
	if err != nil || row.ID != manifest.ID || !artifact.Matches(manifest, config) {
		return registration{Status: "unknown", ErrorCode: "REGISTRATION_CONTENT_CONFLICT"}
	}
	return registration{Status: "registered", ReleaseID: row.ID, RegisteredAt: &row.CreatedAt, Images: manifest.Images}
}
func (s *Server) registrations(runs []githubbuild.Run, config githubbuild.Config) map[string]registration {
	ids := []string{}
	for _, r := range runs {
		ids = append(ids, registrationID(r))
	}
	rows := []ImageRelease{}
	var err error
	if len(ids) > 0 {
		err = s.db.Where("id IN ?", ids).Find(&rows).Error
	}
	byID := map[string]ImageRelease{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	out := map[string]registration{}
	for _, r := range runs {
		id := registrationID(r)
		if err != nil {
			out[id] = registration{Status: "unknown", ErrorCode: "REGISTRATION_STATUS_UNAVAILABLE"}
			continue
		}
		row, exists := byID[id]
		out[id] = registrationFromRow(r, config, row, exists)
	}
	return out
}
func (s *Server) builds(c *gin.Context) {
	if s.github == nil {
		c.JSON(200, gin.H{"enabled": false})
		return
	}
	runs, err := s.github.Runs(c.Request.Context())
	if err != nil {
		writeReleaseError(c, 502, safeBuildCode(err))
		return
	}
	config := s.github.Config()
	registered := s.registrations(runs, config)
	keys, trustErr := s.buildKeys(s.db)
	trustCode := ""
	if trustErr != nil {
		trustCode = trustErr.(*releaseError).Code
	}
	allowed, _ := s.auth.Enforce(c.GetString("admin_role"), "build.write")
	type view struct {
		githubbuild.Run
		Registration registration `json:"registration"`
		CanRegister  bool         `json:"can_register"`
		Reason       string       `json:"reason,omitempty"`
	}
	views := []view{}
	for _, run := range runs {
		reg := registered[registrationID(run)]
		v := view{Run: run, Registration: reg}
		v.CanRegister = allowed && trustErr == nil && reg.Status == "unregistered" && run.Attempt > 0 && run.Status == "completed" && run.Conclusion != nil && *run.Conclusion == "success" && (run.Event == "push" || run.Event == "workflow_dispatch")
		if reg.ErrorCode != "" {
			v.Reason = releaseFailure(reg.ErrorCode).Message
		} else if trustErr != nil {
			v.Reason = trustErr.(*releaseError).Message
		}
		views = append(views, v)
	}
	var dispatches []GitHubDispatch
	if s.db.Order("created_at DESC").Limit(30).Find(&dispatches).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, gin.H{"enabled": true, "config": config, "runs": views, "dispatches": dispatches, "build_trust": gin.H{"key_count": len(keys), "error_code": trustCode}, "services": githubbuild.Services})
}
func (s *Server) registerBuild(c *gin.Context) {
	if s.github == nil {
		c.Status(503)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Attempt int `json:"attempt"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if err != nil || id <= 0 || req.Attempt < 1 {
		c.Status(400)
		return
	}
	// Fetch immutable artifact bytes before holding the shared mutation lock.
	if _, err := s.buildKeys(s.db); err != nil {
		writeReleaseError(c, 409, err.(*releaseError).Code)
		return
	}
	artifact, err := s.github.SignedImages(c.Request.Context(), id)
	if err != nil {
		writeReleaseError(c, 409, safeBuildCode(err))
		return
	}
	s.change(c, body, "build.register", fmt.Sprintf("ci-%d-%d", id, req.Attempt), func(tx *gorm.DB) (any, error) {
		keys, err := s.buildKeys(tx)
		if err != nil {
			return nil, err
		}
		manifest, err := release.Verify(artifact.Release, keys)
		if err != nil {
			return nil, releaseFailure("BUILD_SIGNATURE_UNTRUSTED")
		}
		if artifact.Run.ID != id || artifact.Run.Attempt != req.Attempt || artifact.Attempt != req.Attempt || !artifact.Matches(manifest, s.github.Config()) {
			return nil, releaseFailure("BUILD_PROVENANCE_MISMATCH")
		}
		raw, _ := json.Marshal(artifact.Release)
		var existing ImageRelease
		err = tx.First(&existing, "id = ?", manifest.ID).Error
		if err == nil {
			if existing.Payload != string(raw) {
				return nil, releaseFailure("REGISTRATION_CONTENT_CONFLICT")
			}
			return unchangedMutation{gin.H{"release_id": manifest.ID, "already_registered": true}}, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, releaseFailure("REGISTRATION_STATUS_UNAVAILABLE")
		}
		row := ImageRelease{ID: manifest.ID, Payload: string(raw), CreatedAt: time.Now().UTC()}
		if tx.Create(&row).Error != nil {
			return nil, releaseFailure("REGISTRATION_STATUS_UNAVAILABLE")
		}
		return gin.H{"release_id": manifest.ID, "already_registered": false}, nil
	})
}
func (s *Server) releaseList(c *gin.Context) {
	var rows []ImageRelease
	if s.db.Order("created_at DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	out := []gin.H{}
	for _, row := range rows {
		var signed release.SignedManifest
		if json.Unmarshal([]byte(row.Payload), &signed) != nil {
			writeReleaseError(c, 503, "REGISTRATION_CONTENT_INVALID")
			return
		}
		manifest, err := release.Decode(signed)
		if err != nil || manifest.ID != row.ID {
			writeReleaseError(c, 503, "REGISTRATION_CONTENT_INVALID")
			return
		}
		out = append(out, gin.H{"id": row.ID, "created_at": row.CreatedAt, "manifest": manifest})
	}
	c.JSON(200, out)
}
func (s *Server) dispatchBuild(c *gin.Context) {
	if s.github == nil {
		c.Status(501)
		return
	}
	var req struct{ Ref, Service, Platform string }
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if s.github.ValidateDispatch(req.Ref, req.Service, req.Platform) != nil {
		c.Status(400)
		return
	}
	var id string
	s.change(c, body, "build.dispatch", req.Service, func(tx *gorm.DB) (any, error) {
		value, err := randomToken(24)
		if err != nil {
			return nil, err
		}
		row := GitHubDispatch{ID: value, Status: "uncertain", CreatedAt: time.Now().UTC()}
		if tx.Create(&row).Error != nil {
			return nil, errConflict
		}
		id = value
		return row, nil
	})
	if id != "" && s.github.Dispatch(c.Request.Context(), req.Ref, req.Service, req.Platform) == nil {
		s.db.Model(&GitHubDispatch{}).Where("id = ?", id).Update("status", "accepted")
	}
}

func (s *Server) releaseDetail(c *gin.Context) {
	var row ImageRelease
	err := s.db.First(&row, "id = ?", c.Param("id")).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "Release unavailable"})
		return
	}
	if err != nil {
		writeReleaseError(c, 503, "REGISTRATION_STATUS_UNAVAILABLE")
		return
	}
	var signed release.SignedManifest
	if json.Unmarshal([]byte(row.Payload), &signed) != nil {
		writeReleaseError(c, 503, "REGISTRATION_CONTENT_INVALID")
		return
	}
	manifest, err := release.Decode(signed)
	if err != nil || manifest.ID != row.ID {
		writeReleaseError(c, 503, "REGISTRATION_CONTENT_INVALID")
		return
	}
	c.JSON(200, gin.H{"id": row.ID, "created_at": row.CreatedAt, "manifest": manifest})
}
