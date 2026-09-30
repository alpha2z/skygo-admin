package app

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/release"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"sort"
	"time"
)

type Publication struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	HostID        string    `gorm:"size:64" json:"host_id"`
	ReleaseID     string    `gorm:"size:64" json:"release_id"`
	RequestDigest string    `gorm:"size:64" json:"request_digest"`
	Selection     string    `gorm:"type:text" json:"selection"`
	Scope         string    `gorm:"type:mediumtext" json:"scope"`
	ScopeHash     string    `gorm:"size:64" json:"scope_hash"`
	Envelope      string    `gorm:"type:mediumtext" json:"-"`
	ExecutionID   string    `gorm:"size:64;uniqueIndex" json:"execution_id"`
	RequestedBy   uint32    `json:"requested_by"`
	ApprovedBy    uint32    `json:"approved_by"`
	Status        string    `gorm:"size:32;index" json:"status"`
	Code          string    `gorm:"size:160" json:"code,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}
type publicationRequest struct {
	ID        string   `json:"request_id"`
	HostID    string   `json:"host_id"`
	ReleaseID string   `json:"release_id"`
	Selected  []string `json:"selected"`
}
type publicationScope struct {
	Unit          control.UnitPlan  `json:"unit"`
	Definitions   []control.Service `json:"definitions"`
	ReleaseDigest string            `json:"release_digest"`
}

func publicationError(code, message string) error { return &releaseError{Code: code, Message: message} }
func selectionRequest(c *gin.Context) (publicationRequest, []byte, bool) {
	var req publicationRequest
	raw, err := io.ReadAll(c.Request.Body)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || d.Decode(&req) != nil {
		c.JSON(400, gin.H{"error": "Only request_id, host_id, release_id and selected service IDs are accepted."})
		return req, nil, false
	}
	var extra any
	if d.Decode(&extra) != io.EOF || !control.Identifier.MatchString(req.ID) || !control.Identifier.MatchString(req.HostID) || !control.Identifier.MatchString(req.ReleaseID) || len(req.Selected) < 1 || len(req.Selected) > 2 {
		c.Status(400)
		return req, nil, false
	}
	sort.Strings(req.Selected)
	for i, id := range req.Selected {
		if !control.Identifier.MatchString(id) || (i > 0 && id == req.Selected[i-1]) {
			c.Status(400)
			return req, nil, false
		}
	}
	return req, raw, true
}
func (s *Server) unitInventory(tx *gorm.DB, hostID, executionID string) (Host, []control.Service, []control.Observation, error) {
	var h Host
	var records []ServiceRecord
	fail := func(code, message string) (Host, []control.Service, []control.Observation, error) {
		return h, nil, nil, publicationError(code, message)
	}
	if tx.First(&h, "id = ?", hostID).Error != nil {
		return fail("HOST_MISSING", "Selected host no longer exists.")
	}
	if !h.Active {
		return fail("HOST_REVOKED", "Selected host has been revoked.")
	}
	if time.Since(h.LastSeen) > time.Minute || h.LastSeen.After(time.Now().Add(time.Second)) {
		return fail("HOST_OFFLINE", "Selected host is offline or has a stale heartbeat.")
	}
	if tx.Order("id").Find(&records).Error != nil {
		return fail("INVENTORY_UNAVAILABLE", "Service inventory could not be read.")
	}
	var observations []control.Observation
	if json.Unmarshal([]byte(h.Observations), &observations) != nil {
		return fail("INVENTORY_UNAVAILABLE", "Host inventory is unavailable.")
	}
	defs := []control.Service{}
	seen := []control.Observation{}
	for _, kind := range []string{"admin-api", "admin-web"} {
		var def control.Service
		matches := 0
		for _, r := range records {
			var d control.Service
			if json.Unmarshal([]byte(r.Definition), &d) != nil {
				return fail("INVENTORY_UNAVAILABLE", "Service definition is invalid.")
			}
			if d.HostID == hostID && d.ControlPlane && d.ControlKind == kind {
				matches++
				def = d
				if r.BusyTask != "" && r.BusyTask != executionID {
					return fail("SERVICE_BUSY", "Service "+d.ID+" is locked by task "+r.BusyTask+".")
				}
			}
		}
		if matches != 1 {
			return fail("UNIT_MEMBERSHIP_INVALID", "Host needs exactly one API and one Web control service.")
		}
		var o control.Observation
		matches = 0
		for _, candidate := range observations {
			if candidate.Service == def.ID {
				matches++
				o = candidate
			}
		}
		capable := false
		for _, cap := range o.Capabilities {
			capable = capable || cap == "control.unit.v1"
		}
		if matches != 1 || !capable || o.ControlKind != kind || !control.Identifier.MatchString(o.UnitID) || len(o.InventoryRevision) != 64 || !control.ImageIdentity(o.ImageID) {
			return fail("UNIT_CAPABILITY_MISSING", "Service "+def.ID+" requires an explicitly configured control.unit.v1 agent.")
		}
		if o.SyncImageEnv && !hasCapability(o, "image.env.v1") {
			return fail("ENV_CAPABILITY_MISSING", "Upgrade the Agent before enabling environment persistence.")
		}
		if !o.Healthy {
			return fail("SERVICE_UNHEALTHY", "Service "+def.ID+" is not healthy.")
		}
		if !release.Platform(def.Platform) || o.Platform != def.Platform {
			return fail("PLATFORM_MISMATCH", "Service "+def.ID+" architecture differs from inventory.")
		}
		defs = append(defs, def)
		seen = append(seen, o)
	}
	if seen[0].UnitID != seen[1].UnitID || seen[0].Platform != seen[1].Platform {
		return fail("UNIT_MEMBERSHIP_INVALID", "API and Web must share the declared local recovery unit and platform.")
	}
	for _, d := range defs {
		for _, dependency := range d.DependsOn {
			if dependency != defs[0].ID && dependency != defs[1].ID && !s.healthy(tx, dependency) {
				return fail("DEPENDENCY_UNHEALTHY", "Service "+d.ID+" dependency "+dependency+" is not healthy.")
			}
		}
	}
	// External running dependents would be disrupted outside the approved unit.
	for _, r := range records {
		var d control.Service
		json.Unmarshal([]byte(r.Definition), &d)
		if d.ID == defs[0].ID || d.ID == defs[1].ID {
			continue
		}
		for _, dep := range d.DependsOn {
			if (dep == defs[0].ID || dep == defs[1].ID) && s.healthy(tx, d.ID) {
				return fail("EXTERNAL_DEPENDENT", "Running service "+d.ID+" depends on this recovery unit.")
			}
		}
	}
	for _, dep := range defs[0].DependsOn {
		if dep == defs[1].ID {
			defs[0], defs[1] = defs[1], defs[0]
			seen[0], seen[1] = seen[1], seen[0]
			break
		}
	}
	return h, defs, seen, nil
}
func (s *Server) resolvePublication(tx *gorm.DB, req publicationRequest, executionID string) (publicationScope, error) {
	scope := publicationScope{}
	m, err := s.trustedRelease(tx, req.ReleaseID)
	if err != nil {
		return scope, err
	}
	h, defs, seen, err := s.unitInventory(tx, req.HostID, executionID)
	if err != nil {
		return scope, err
	}
	group, err := s.prepareGroup(tx, m, h, time.Now().UTC(), req.Selected...)
	if err != nil {
		return scope, err
	}
	if group.Status == "blocked" {
		return scope, releaseFailure(group.ErrorCode)
	}
	if group.Status != "ready" {
		detail := "Selected images need fresh preparation receipts."
		for _, t := range group.Targets {
			if t.Status != "ready" {
				detail += " Service " + t.Service + ": " + t.Status + " (task " + t.TaskID + ")."
			}
		}
		return scope, publicationError("PREPARATION_REQUIRED", detail)
	}
	scope.Definitions = defs
	b, _ := json.Marshal(m)
	scope.ReleaseDigest = control.Digest(b)
	scope.Unit = control.UnitPlan{ID: seen[0].UnitID, BootID: h.BootID}
	for i, d := range defs {
		t := control.UnitTarget{SyncImageEnv: seen[i].SyncImageEnv, Service: d.ID, Kind: d.ControlKind, Platform: d.Platform, PreviousImageID: seen[i].ImageID, Image: seen[i].ImageID, ImageID: seen[i].ImageID, Revision: seen[i].InventoryRevision}
		for _, prepared := range group.Targets {
			if prepared.Service == d.ID {
				t.Selected = true
				t.Image = prepared.Image
				t.ImageID = prepared.ImageID
				if t.ImageID == t.PreviousImageID {
					return scope, publicationError("IMAGE_UNCHANGED", "Service "+d.ID+" already runs this image; deselect it.")
				}
			}
		}
		scope.Unit.Targets = append(scope.Unit.Targets, t)
	}
	if err := scope.Unit.Validate(defs[0].ID); err != nil {
		return scope, publicationError("SCOPE_INVALID", "Recovery scope could not be validated.")
	}
	return scope, nil
}
func (s *Server) prepareSelection(c *gin.Context) {
	req, body, ok := selectionRequest(c)
	if !ok {
		return
	}
	s.change(c, body, "publication.prepare", req.ID, func(tx *gorm.DB) (any, error) {
		h, _, _, err := s.unitInventory(tx, req.HostID, "")
		if err != nil {
			return nil, err
		}
		m, err := s.trustedRelease(tx, req.ReleaseID)
		if err != nil {
			return nil, err
		}
		g, err := s.prepareGroup(tx, m, h, time.Now().UTC(), req.Selected...)
		if err != nil {
			return nil, err
		}
		if g.Status == "blocked" {
			return nil, releaseFailure(g.ErrorCode)
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
			cmd := control.Command{Version: 1, ID: id, HostID: h.ID, Service: t.Service, Action: "prepare-image", Image: t.Image, Platform: t.Platform, ExpiresAt: time.Now().UTC().Add(time.Hour)}
			if err := cmd.Validate(); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(cmd)
			row := Task{ID: id, HostID: h.ID, ServiceID: t.Service, Action: cmd.Action, Payload: string(raw), PayloadHash: control.Digest(raw), Status: "queued", RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: time.Now().UTC(), ExpiresAt: cmd.ExpiresAt}
			if err := s.queuePreparation(tx, &row, m.ID, h).Error; err != nil {
				return nil, err
			}
			if err := tx.Save(&ImagePreparation{ID: preparationID(h.ID, t), TaskID: id, HostBootID: h.BootID}).Error; err != nil {
				return nil, err
			}
			jobs = append(jobs, id)
			changed = true
		}
		out := gin.H{"tasks": jobs, "service_changes": false}
		if !changed {
			return unchangedMutation{out}, nil
		}
		return out, nil
	})
}
func (s *Server) createPublication(c *gin.Context) {
	req, body, ok := selectionRequest(c)
	if !ok {
		return
	}
	s.change(c, body, "publication.create", req.ID, func(tx *gorm.DB) (any, error) {
		normalized, _ := json.Marshal(req)
		digest := control.Digest(normalized)
		var old Publication
		err := tx.First(&old, "id = ?", req.ID).Error
		if err == nil {
			if old.RequestDigest != digest || old.RequestedBy != uint32(c.GetUint("admin_id")) {
				return nil, publicationError("REQUEST_CONFLICT", "Request ID already belongs to a different publication request.")
			}
			return unchangedMutation{old}, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		scope, err := s.resolvePublication(tx, req, "")
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(scope)
		selected, _ := json.Marshal(req.Selected)
		now := time.Now().UTC()
		p := Publication{ID: req.ID, HostID: req.HostID, ReleaseID: req.ReleaseID, RequestDigest: digest, Selection: string(selected), Scope: string(raw), ScopeHash: control.Digest(raw), ExecutionID: "unit-" + control.Digest([]byte(req.ID))[:48], RequestedBy: uint32(c.GetUint("admin_id")), Status: "pending", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
		cmd := control.Command{Version: 1, ID: p.ExecutionID, HostID: p.HostID, Service: scope.Unit.Targets[0].Service, Action: "control-unit", Unit: &scope.Unit, ExpiresAt: p.ExpiresAt}
		envelope, err := control.Sign(cmd, s.cfg.SigningKey)
		if err != nil {
			return nil, err
		}
		signed, _ := json.Marshal(envelope)
		p.Envelope = string(signed)
		return p, tx.Create(&p).Error
	})
}
func (s *Server) publicationActor(tx *gorm.DB, id uint32, permission string) bool {
	var user AdminUser
	if tx.First(&user, id).Error != nil || !user.Active {
		return false
	}
	allowed, err := s.auth.Enforce(user.Role, permission)
	return err == nil && allowed
}
func (s *Server) recheckPublication(tx *gorm.DB, p Publication) error {
	if !s.publicationActor(tx, p.RequestedBy, "ops.write") || !s.publicationActor(tx, p.RequestedBy, "build.read") || (p.ApprovedBy != 0 && !s.publicationActor(tx, p.ApprovedBy, "ops.approve")) {
		return publicationError("AUTHORITY_CHANGED", "Requester or approver permissions are no longer valid.")
	}
	req := publicationRequest{ID: p.ID, HostID: p.HostID, ReleaseID: p.ReleaseID}
	if json.Unmarshal([]byte(p.Selection), &req.Selected) != nil {
		return errConflict
	}
	scope, err := s.resolvePublication(tx, req, p.ExecutionID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(scope)
	if control.Digest(raw) != p.ScopeHash || string(raw) != p.Scope {
		return publicationError("SCOPE_CHANGED", "Host boot, local configuration, service definition, dependency, release or current image changed. Create a new reviewed request.")
	}
	var envelope control.Envelope
	if json.Unmarshal([]byte(p.Envelope), &envelope) != nil {
		return errConflict
	}
	cmd, err := control.Verify(envelope, s.cfg.SigningKey.Public().(ed25519.PublicKey), p.HostID)
	if err != nil || cmd.ID != p.ExecutionID || cmd.Unit == nil {
		return errConflict
	}
	unit, _ := json.Marshal(cmd.Unit)
	expected, _ := json.Marshal(scope.Unit)
	if !bytes.Equal(unit, expected) {
		return errConflict
	}
	return nil
}
func (s *Server) approvePublication(c *gin.Context) {
	s.change(c, nil, "publication.approve", c.Param("id"), func(tx *gorm.DB) (any, error) {
		var p Publication
		if tx.First(&p, "id = ?", c.Param("id")).Error != nil {
			return nil, errConflict
		}
		approver := uint32(c.GetUint("admin_id"))
		if p.RequestedBy == approver {
			return nil, publicationError("INDEPENDENT_APPROVAL_REQUIRED", "A different administrator must approve this publication.")
		}
		if p.Status != "pending" {
			if p.ApprovedBy == approver && p.ApprovedBy != 0 && p.Status != "rejected" && p.Status != "expired" {
				return unchangedMutation{p}, nil
			}
			return nil, errConflict
		}
		if time.Now().After(p.ExpiresAt) {
			return nil, publicationError("PUBLICATION_EXPIRED", "Publication approval window expired.")
		}
		p.ApprovedBy = approver
		if err := s.recheckPublication(tx, p); err != nil {
			return nil, err
		}
		var active Task
		err := tx.Where("status IN ?", []string{"queued", "dispatched", "uncertain"}).First(&active).Error
		if err == nil {
			return nil, publicationError("TASK_BLOCKING", "Task "+active.ID+" must finish before this control-plane publication.")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		var envelope control.Envelope
		json.Unmarshal([]byte(p.Envelope), &envelope)
		var cmd control.Command
		json.Unmarshal(envelope.Payload, &cmd)
		for _, t := range cmd.Unit.Targets {
			result := tx.Model(&ServiceRecord{}).Where("id = ? AND busy_task = ?", t.Service, "").Update("busy_task", p.ExecutionID)
			if result.Error != nil || result.RowsAffected != 1 {
				return nil, errConflict
			}
		}
		task := Task{ID: p.ExecutionID, PublicationID: p.ID, HostID: p.HostID, ServiceID: cmd.Service, Action: cmd.Action, Payload: string(envelope.Payload), PayloadHash: control.Digest(envelope.Payload), Status: "queued", RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, CreatedAt: time.Now().UTC(), ExpiresAt: p.ExpiresAt}
		if err := tx.Create(&task).Error; err != nil {
			return nil, err
		}
		p.Status = "queued"
		return p, tx.Save(&p).Error
	})
}
func (s *Server) rejectPublication(c *gin.Context) {
	s.change(c, nil, "publication.reject", c.Param("id"), func(tx *gorm.DB) (any, error) {
		var p Publication
		if tx.First(&p, "id = ?", c.Param("id")).Error != nil || p.Status != "pending" {
			return nil, errConflict
		}
		p.Status = "rejected"
		p.ApprovedBy = uint32(c.GetUint("admin_id"))
		return p, tx.Save(&p).Error
	})
}
func (s *Server) publications(c *gin.Context) {
	rows := []Publication{}
	if s.db.Order("created_at DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) publicationDetail(c *gin.Context) {
	var row Publication
	err := s.db.First(&row, "id = ?", c.Param("id")).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Status(404)
		return
	}
	if err != nil {
		c.Status(503)
		return
	}
	c.JSON(200, row)
}
func (s *Server) publicationState(tx *gorm.DB, t Task, status, code string) error {
	if t.PublicationID == "" {
		return nil
	}
	if status != "queued" && status != "dispatched" && status != "uncertain" {
		if err := tx.Model(&ServiceRecord{}).Where("busy_task = ?", t.ID).Update("busy_task", "").Error; err != nil {
			return err
		}
	}
	return tx.Model(&Publication{}).Where("id = ? AND execution_id = ?", t.PublicationID, t.ID).Updates(map[string]any{"status": status, "code": code}).Error
}
func validUnitResult(c control.Command, r control.Result) bool {
	if c.Unit == nil {
		return false
	}
	if r.Status == "uncertain" {
		return true
	}
	if r.Status == "failed" {
		return r.Code == "UNIT_PREFLIGHT_REJECTED" && len(r.Unit) == 0
	}
	if r.Status != "succeeded" && r.Status != "rolled_back" || len(r.Unit) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, proof := range r.Unit {
		if seen[proof.Service] || !proof.Healthy {
			return false
		}
		seen[proof.Service] = true
		match := false
		for _, target := range c.Unit.Targets {
			id := target.ImageID
			if r.Status == "rolled_back" {
				id = target.PreviousImageID
			}
			match = match || (proof.Service == target.Service && proof.ImageID == id)
		}
		if !match {
			return false
		}
	}
	return true
}

func (s *Server) publicationCandidates(c *gin.Context) {
	m, err := s.trustedRelease(s.db, c.Query("release_id"))
	if err != nil {
		e := err.(*releaseError)
		c.JSON(409, gin.H{"error": e.Message, "error_code": e.Code})
		return
	}
	hosts := []Host{}
	if s.db.Order("id").Find(&hosts).Error != nil {
		c.Status(503)
		return
	}
	rows := []gin.H{}
	for _, host := range hosts {
		row := gin.H{"host_id": host.ID, "active": host.Active, "last_seen": host.LastSeen, "targets": []gin.H{}}
		h, defs, seen, err := s.unitInventory(s.db, host.ID, "")
		if err != nil {
			e := err.(*releaseError)
			row["error_code"] = e.Code
			row["reason"] = e.Message
			rows = append(rows, row)
			continue
		}
		targets := []gin.H{}
		for i, d := range defs {
			target := gin.H{"service": d.ID, "kind": d.ControlKind, "current_image_id": seen[i].ImageID, "current_image": seen[i].Image, "platform": d.Platform, "available": false, "changed": false, "preparation": "unavailable", "provenance": s.imageProvenance(s.db, h, d, seen[i])}
			for _, image := range m.Images {
				if image.Service == d.ControlKind && image.Platform == d.Platform {
					target["image"] = image.Reference
					target["available"] = true
					target["changed"] = seen[i].Image != image.Reference
					g, err := s.prepareGroup(s.db, m, h, time.Now().UTC(), d.ID)
					if err != nil {
						c.Status(503)
						return
					}
					target["preparation"] = g.Status
					if len(g.Targets) == 1 {
						target["task_id"] = g.Targets[0].TaskID
						if g.Targets[0].ImageID != "" {
							target["changed"] = g.Targets[0].ImageID != seen[i].ImageID
						}
					}
				}
			}
			targets = append(targets, target)
		}
		row["targets"] = targets
		rows = append(rows, row)
	}
	c.JSON(200, gin.H{"hosts": rows, "host_count": len(hosts), "release_id": m.ID})
}

// Match immutable references or verified image-cache receipts, never tags.
func (s *Server) imageProvenance(tx *gorm.DB, h Host, d control.Service, o control.Observation) any {
	refs := map[string]bool{}
	if control.Image.MatchString(o.Image) {
		refs[o.Image] = true
	}
	var tasks []Task
	if tx.Where("host_id = ? AND service_id = ? AND action = ? AND status = ?", h.ID, d.ID, "prepare-image", "succeeded").Find(&tasks).Error != nil {
		return gin.H{"status": "unknown"}
	}
	for _, t := range tasks {
		var r control.Result
		if json.Unmarshal([]byte(t.Result), &r) == nil && r.ImageID == o.ImageID && r.Platform == o.Platform {
			refs[r.Image] = true
		}
	}
	var rows []ImageRelease
	if tx.Order("created_at DESC").Find(&rows).Error != nil {
		return gin.H{"status": "unknown"}
	}
	for _, row := range rows {
		m, err := s.trustedRelease(tx, row.ID)
		if err != nil {
			continue
		}
		for _, image := range m.Images {
			if image.Service == d.ControlKind && image.Platform == o.Platform && refs[image.Reference] {
				return gin.H{"status": "matched", "release_id": row.ID, "ref": m.Build.Ref, "commit": m.Build.SourceCommit, "registered_at": row.CreatedAt}
			}
		}
	}
	return gin.H{"status": "unmatched"}
}

func hasCapability(o control.Observation, name string) bool {
	for _, c := range o.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}
