package app

import (
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/release"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"regexp"
	"sort"
	"time"
)

const preparationTTL = 5 * time.Minute

var runtimeImagePattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validRuntimeImageID(s string) bool { return runtimeImagePattern.MatchString(s) }

type prepareTarget struct {
	Service      string     `json:"service"`
	Kind         string     `json:"kind"`
	Image        string     `json:"image"`
	Platform     string     `json:"platform"`
	CurrentImage string     `json:"current_image"`
	TaskID       string     `json:"task_id,omitempty"`
	Status       string     `json:"status"`
	ImageID      string     `json:"image_id,omitempty"`
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
	ErrorCode    string     `json:"error_code,omitempty"`
}
type prepareGroup struct {
	HostID     string          `json:"host_id"`
	ReleaseID  string          `json:"release_id"`
	Batch      string          `json:"batch"`
	Status     string          `json:"status"`
	ErrorCode  string          `json:"error_code,omitempty"`
	Reason     string          `json:"reason"`
	CanPrepare bool            `json:"can_prepare"`
	Targets    []prepareTarget `json:"targets"`
}

func preparationID(host string, t prepareTarget) string {
	return control.Digest([]byte(host + "\n" + t.Service + "\n" + t.Platform + "\n" + t.Image))
}
func (s *Server) trustedRelease(tx *gorm.DB, id string) (release.Manifest, error) {
	var row ImageRelease
	if tx.First(&row, "id = ?", id).Error != nil {
		return release.Manifest{}, releaseFailure("REGISTRATION_STATUS_UNAVAILABLE")
	}
	var signed release.SignedManifest
	if json.Unmarshal([]byte(row.Payload), &signed) != nil {
		return release.Manifest{}, releaseFailure("REGISTRATION_CONTENT_INVALID")
	}
	keys, err := s.buildKeys(tx)
	if err != nil {
		return release.Manifest{}, err
	}
	manifest, err := release.Verify(signed, keys)
	if err != nil || manifest.ID != id {
		return release.Manifest{}, releaseFailure("BUILD_SIGNATURE_UNTRUSTED")
	}
	return manifest, nil
}
func (s *Server) prepareGroup(tx *gorm.DB, m release.Manifest, h Host, now time.Time, selected ...string) (prepareGroup, error) {
	g := prepareGroup{HostID: h.ID, ReleaseID: m.ID, Status: "unprepared", Targets: []prepareTarget{}}
	block := func(code string) (prepareGroup, error) {
		g.Status = "blocked"
		g.ErrorCode = code
		g.Reason = releaseFailure(code).Message
		return g, nil
	}
	if !h.Active || now.Sub(h.LastSeen) > time.Minute || h.LastSeen.After(now.Add(time.Second)) {
		return block("ADMIN_HOST_OFFLINE")
	}
	var services []ServiceRecord
	if tx.Find(&services).Error != nil {
		return g, releaseFailure("REGISTRATION_STATUS_UNAVAILABLE")
	}
	byKind := map[string]control.Service{}
	for _, r := range services {
		var def control.Service
		if json.Unmarshal([]byte(r.Definition), &def) != nil {
			return g, releaseFailure("REGISTRATION_CONTENT_INVALID")
		}
		if def.HostID != h.ID || !def.ControlPlane || (def.ControlKind != "admin-api" && def.ControlKind != "admin-web") {
			continue
		}
		if _, ok := byKind[def.ControlKind]; ok {
			return block("ADMIN_TARGET_AMBIGUOUS")
		}
		byKind[def.ControlKind] = def
	}
	var observations []control.Observation
	if json.Unmarshal([]byte(h.Observations), &observations) != nil {
		return block("ADMIN_CAPABILITY_MISSING")
	}
	platform := ""
	for _, kind := range []string{"admin-api", "admin-web"} {
		def, ok := byKind[kind]
		if !ok {
			return block("ADMIN_TARGET_MISSING")
		}
		if len(selected) > 0 {
			match := false
			for _, id := range selected {
				match = match || id == def.ID
			}
			if !match {
				continue
			}
		}
		var seen *control.Observation
		for i := range observations {
			if observations[i].Service == def.ID {
				if seen != nil {
					return block("ADMIN_TARGET_AMBIGUOUS")
				}
				seen = &observations[i]
			}
		}
		if seen == nil || !validRuntimeImageID(seen.ImageID) {
			return block("ADMIN_CAPABILITY_MISSING")
		}
		capable := false
		for _, c := range seen.Capabilities {
			capable = capable || c == "image.prepare.v1"
		}
		if !capable {
			return block("ADMIN_CAPABILITY_MISSING")
		}
		if !release.Platform(def.Platform) || seen.Platform != def.Platform || (platform != "" && platform != def.Platform) {
			return block("ADMIN_PLATFORM_MISMATCH")
		}
		platform = def.Platform
		var image release.Image
		for _, candidate := range m.Images {
			if candidate.Service == kind && candidate.Platform == platform {
				image = candidate
			}
		}
		if image.Reference == "" {
			return block("ADMIN_PAIR_MISSING")
		}
		g.Targets = append(g.Targets, prepareTarget{Service: def.ID, Kind: kind, Image: image.Reference, Platform: platform, CurrentImage: seen.Image, Status: "unprepared"})
	}
	if len(selected) > 0 && len(g.Targets) != len(selected) {
		return block("ADMIN_TARGET_MISSING")
	}
	identity := []string{m.ID, h.ID}
	for _, t := range g.Targets {
		identity = append(identity, t.Service, t.Image, t.Platform)
	}
	b, _ := json.Marshal(identity)
	g.Batch = control.Digest(b)
	ready, busy, failed, unknown := 0, 0, 0, 0
	for i := range g.Targets {
		target := &g.Targets[i]
		var p ImagePreparation
		err := tx.First(&p, "id = ?", preparationID(h.ID, *target)).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return g, releaseFailure("REGISTRATION_STATUS_UNAVAILABLE")
		}
		var task Task
		if tx.First(&task, "id = ?", p.TaskID).Error != nil {
			return g, releaseFailure("REGISTRATION_STATUS_UNAVAILABLE")
		}
		target.TaskID = task.ID
		switch {
		case (task.Status == "queued" || task.Status == "dispatched") && now.Before(task.ExpiresAt):
			target.Status = "working"
			busy++
		case task.Status == "succeeded" && p.HostBootID == h.BootID:
			var result control.Result
			if json.Unmarshal([]byte(task.Result), &result) == nil && result.Image == target.Image && result.Platform == platform && validRuntimeImageID(result.ImageID) && task.FinishedAt != nil && now.Sub(*task.FinishedAt) <= preparationTTL && !task.FinishedAt.After(now.Add(time.Second)) {
				target.Status = "ready"
				target.ImageID = result.ImageID
				target.VerifiedAt = task.FinishedAt
				ready++
			} else {
				target.Status = "unknown"
				unknown++
			}
		case task.Status == "succeeded":
			target.Status = "unknown"
			unknown++
		default:
			target.Status = "failed"
			var result control.Result
			if json.Unmarshal([]byte(task.Result), &result) == nil {
				target.ErrorCode = result.Code
			}
			failed++
		}
	}
	switch {
	case ready == len(g.Targets) && ready > 0:
		g.Status = "ready"
		g.Reason = "Both images are prepared; services have not been upgraded."
	case busy > 0:
		g.Status = "working"
		g.Reason = "Preparing images without restarting services."
	case failed > 0:
		g.Status = "failed"
		g.Reason = "Retry failed items; fresh successful receipts will be kept."
	case unknown > 0:
		g.Status = "unknown"
		g.Reason = "Receipts are stale; inspect images again before proceeding."
	default:
		g.Reason = "Prepare the paired API and Web images without changing containers."
	}
	g.CanPrepare = g.Status != "working" && g.Status != "ready"
	return g, nil
}
func (s *Server) adminPreparation(c *gin.Context) {
	m, err := s.trustedRelease(s.db, c.Param("id"))
	if err != nil {
		writeReleaseError(c, 409, err.(*releaseError).Code)
		return
	}
	var hosts []Host
	if s.db.Order("id").Limit(200).Find(&hosts).Error != nil {
		c.Status(503)
		return
	}
	allowed, _ := s.auth.Enforce(c.GetString("admin_role"), "ops.write")
	groups := []prepareGroup{}
	for _, h := range hosts {
		g, err := s.prepareGroup(s.db, m, h, time.Now().UTC())
		if err != nil {
			writeReleaseError(c, 503, "REGISTRATION_STATUS_UNAVAILABLE")
			return
		}
		g.CanPrepare = g.CanPrepare && allowed
		groups = append(groups, g)
	}
	c.JSON(200, groups)
}
func (s *Server) prepareAdminImages(c *gin.Context) {
	var req struct {
		HostID string `json:"host_id"`
		Batch  string `json:"batch"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	s.change(c, body, "images.prepare", c.Param("id"), func(tx *gorm.DB) (any, error) {
		m, err := s.trustedRelease(tx, c.Param("id"))
		if err != nil {
			return nil, err
		}
		var h Host
		if tx.First(&h, "id = ?", req.HostID).Error != nil {
			return nil, releaseFailure("ADMIN_TARGET_MISSING")
		}
		now := time.Now().UTC()
		g, err := s.prepareGroup(tx, m, h, now)
		if err != nil {
			return nil, err
		}
		if g.Status == "blocked" {
			return nil, releaseFailure(g.ErrorCode)
		}
		if req.Batch != g.Batch {
			return nil, releaseFailure("ADMIN_PREVIEW_CHANGED")
		}
		jobs := []string{}
		changed := false
		for _, t := range g.Targets {
			if t.Status == "ready" || t.Status == "working" {
				jobs = append(jobs, t.TaskID)
				continue
			}
			id, err := randomToken(24)
			if err != nil {
				return nil, err
			}
			command := control.Command{Version: 1, ID: id, HostID: h.ID, Service: t.Service, Action: "prepare-image", Image: t.Image, Platform: t.Platform, ExpiresAt: now.Add(time.Hour)}
			if command.Validate() != nil {
				return nil, errConflict
			}
			raw, _ := json.Marshal(command)
			task := Task{ID: id, HostID: h.ID, ServiceID: t.Service, Action: command.Action, Payload: string(raw), PayloadHash: control.Digest(raw), Status: "queued", RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: now, ExpiresAt: command.ExpiresAt}
			if tx.Create(&task).Error != nil {
				return nil, errConflict
			}
			if tx.Save(&ImagePreparation{ID: preparationID(h.ID, t), TaskID: id, HostBootID: h.BootID}).Error != nil {
				return nil, errConflict
			}
			jobs = append(jobs, id)
			changed = true
		}
		sort.Strings(jobs)
		out := gin.H{"batch": g.Batch, "tasks": jobs, "service_changes": false}
		if !changed {
			return unchangedMutation{out}, nil
		}
		return out, nil
	})
}
