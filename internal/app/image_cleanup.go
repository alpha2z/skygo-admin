package app

import (
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/imagecontract"
	"github.com/alpha2z/skygo-admin/internal/registrycache"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type CleanupResource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	HostID   string `json:"host_id,omitempty"`
	Service  string `json:"service"`
	Image    string `json:"image"`
	ImageID  string `json:"image_id"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256,omitempty"`
	CacheID  string `json:"cache_id,omitempty"`
	Size     int64  `json:"size"`
	Reason   string `json:"reason,omitempty"`
}
type ImageCleanup struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	Scope       string    `gorm:"type:mediumtext" json:"scope"`
	ScopeHash   string    `gorm:"size:64" json:"scope_hash"`
	Status      string    `gorm:"size:32;index" json:"status"`
	RequestedBy uint32    `json:"requested_by"`
	ApprovedBy  uint32    `json:"approved_by"`
	TaskID      string    `gorm:"size:64" json:"task_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Code        string    `gorm:"size:64" json:"code,omitempty"`
}

func resourceID(r CleanupResource) string {
	b, _ := json.Marshal([]string{r.HostID, r.Service, r.Kind, r.Image, r.ImageID, r.SHA256})
	return control.Digest(b)
}
func (s *Server) cleanupResources(tx *gorm.DB) ([]CleanupResource, error) {
	protected := map[string]string{}
	hosts := []Host{}
	tasks := []Task{}
	deliveries := []ImageDelivery{}
	releases := []ImageRelease{}
	pubs := []Publication{}
	caches := []ImageCache{}
	for _, dest := range []any{&hosts, &tasks, &deliveries, &releases, &pubs, &caches} {
		if err := tx.Find(dest).Error; err != nil {
			return nil, err
		}
	}
	protect := func(key, reason string) {
		if key != "" {
			protected[key] = reason
		}
	}
	online := map[string]bool{}
	capable := map[string]bool{}
	for _, h := range hosts {
		online[h.ID] = h.Active && time.Since(h.LastSeen) < time.Minute
		var observations []control.Observation
		if json.Unmarshal([]byte(h.Observations), &observations) != nil {
			return nil, publicationError("CLEANUP_STATE_UNKNOWN", "Host inventory is unreadable; cleanup is disabled.")
		}
		for _, o := range observations {
			protect(o.Image, "Current container reference")
			protect(o.ImageID, "Current container reference")
			for _, cap := range o.Capabilities {
				if cap == "image.cleanup.v1" {
					capable[h.ID+"/"+o.Service] = true
				}
			}
		}
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].CreatedAt.After(releases[j].CreatedAt) })
	counts := map[string]int{}
	for _, row := range releases {
		m, err := s.trustedRelease(tx, row.ID)
		if err != nil {
			return nil, err
		}
		for _, image := range m.Images {
			key := image.Service + "/" + image.Platform
			counts[key]++
			if counts[key] <= 3 {
				protect(image.Reference, "Keep latest three versions")
			}
		}
	}
	for _, d := range deliveries {
		if !online[d.HostID] || d.Status == "preparing" || d.Status == "queued" || d.Status == "working" || d.Status == "uncertain" {
			protect(d.Image, "Offline host or unresolved delivery")
		}
		if time.Since(d.UpdatedAt) < 7*24*time.Hour {
			protect(d.Image, "Used within seven days")
		}
	}
	for _, t := range tasks {
		if t.Action == "image-cleanup" {
			continue
		}
		var c control.Command
		if json.Unmarshal([]byte(t.Payload), &c) != nil {
			if t.Payload == "" {
				continue
			}
			return nil, errConflict
		}
		if t.Status == "pending" || t.Status == "queued" || t.Status == "preparing" || t.Status == "dispatched" || t.Status == "uncertain" {
			protect(c.Image, "Unresolved task")
			protect(c.PreviousImageID, "Rollback image required")
			if c.Unit != nil {
				for _, target := range c.Unit.Targets {
					protect(target.Image, "Unresolved publication")
					protect(target.PreviousImageID, "Rollback image required")
					protect(target.ImageID, "Unresolved publication")
				}
			}
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	lastTasks := map[string]int{}
	for _, t := range tasks {
		if t.Action != "deploy" && t.Action != "rollback" {
			continue
		}
		key := t.HostID + "/" + t.ServiceID
		lastTasks[key]++
		if lastTasks[key] <= 2 {
			var cmd control.Command
			if json.Unmarshal([]byte(t.Payload), &cmd) != nil {
				return nil, errConflict
			}
			protect(cmd.Image, "Previous service deployment")
			protect(cmd.PreviousImageID, "Previous rollback image")
		}
	}
	sort.Slice(pubs, func(i, j int) bool { return pubs[i].CreatedAt.After(pubs[j].CreatedAt) })
	latest := map[string]bool{}
	for _, p := range pubs {
		var scope publicationScope
		if json.Unmarshal([]byte(p.Scope), &scope) != nil {
			return nil, errConflict
		}
		for _, t := range scope.Unit.Targets {
			key := p.HostID + "/" + t.Service
			if !latest[key] || time.Since(p.CreatedAt) < 7*24*time.Hour {
				protect(t.PreviousImageID, "Previous rollback set")
				protect(t.ImageID, "Recent publication image")
				protect(t.Image, "Recent publication image")
			}
			latest[key] = true
		}
	}
	var definitions []ServiceRecord
	if tx.Find(&definitions).Error != nil {
		return nil, errConflict
	}
	for _, row := range definitions {
		var def control.Service
		if json.Unmarshal([]byte(row.Definition), &def) != nil {
			return nil, errConflict
		}
		protect(def.Image, "Configured service image")
	}
	rows := []CleanupResource{}
	seen := map[string]bool{}
	add := func(r CleanupResource) {
		r.ID = resourceID(r)
		if seen[r.ID] {
			return
		}
		seen[r.ID] = true
		for _, key := range []string{r.Image, r.ImageID, r.SHA256} {
			if reason := protected[key]; reason != "" {
				r.Reason = reason
				break
			}
		}
		if r.HostID != "" && (!online[r.HostID] || !capable[r.HostID+"/"+r.Service]) {
			r.Reason = "Host offline or cleanup capability unavailable"
		}
		rows = append(rows, r)
	}
	sharedArchives := map[string]int{}
	for _, cache := range caches {
		sharedArchives[cache.SHA256]++
	}
	for digest, count := range sharedArchives {
		if count > 1 {
			protect(digest, "Shared archive reference")
		}
	}
	for _, cache := range caches {
		resource := CleanupResource{Kind: "central", Service: cache.Service, Image: cache.Image, ImageID: cache.ImageID, Platform: cache.Platform, SHA256: cache.SHA256, Size: cache.Size, CacheID: cache.ID}
		if s.distribution == nil {
			resource.Reason = "Central distribution disabled"
		} else if _, err := registrycache.ReadDownloadCache(s.distribution.Directory, cache.ID); err != nil {
			resource.Reason = "Central cache index unknown"
		}
		add(resource)
	}
	for _, d := range deliveries {
		for _, t := range tasks {
			if t.DeliveryID != d.ID || t.Status != "succeeded" {
				continue
			}
			var result control.Result
			if json.Unmarshal([]byte(t.Result), &result) != nil || !control.ImageIdentity(result.ImageID) {
				continue
			}
			r := CleanupResource{Kind: "image", HostID: d.HostID, Service: d.Service, Image: d.Image, ImageID: result.ImageID, Platform: d.Platform}
			add(r)
			var command control.Command
			if json.Unmarshal([]byte(t.Payload), &command) == nil && command.Archive != nil {
				r.Kind = "archive"
				r.SHA256 = command.Archive.SHA256
				r.Size = command.Archive.Size
				add(r)
			}
		}
	}
	// Identical runtime content/archives inherit all references, including aliases.
	for _, r := range rows {
		if r.Reason != "" {
			protect(r.ImageID, r.Reason)
			protect(r.SHA256, r.Reason)
		}
	}
	for i := range rows {
		for _, key := range []string{rows[i].ImageID, rows[i].SHA256} {
			if protected[key] != "" {
				rows[i].Reason = protected[key]
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}
func (s *Server) cleanupInventory(c *gin.Context) {
	rows, err := s.cleanupResources(s.db)
	if err != nil {
		c.JSON(409, gin.H{"error": "Cleanup references are unknown; no content may be removed."})
		return
	}
	c.JSON(200, gin.H{"resources": rows, "keep_versions": 3, "keep_days": 7})
}
func (s *Server) previewCleanup(c *gin.Context) {
	var req struct {
		ID         string `json:"request_id"`
		ResourceID string `json:"resource_id"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if !control.Identifier.MatchString(req.ID) {
		c.Status(400)
		return
	}
	s.change(c, body, "images.cleanup.preview", req.ID, func(tx *gorm.DB) (any, error) {
		var old ImageCleanup
		err := tx.First(&old, "id = ?", req.ID).Error
		if err == nil {
			var r CleanupResource
			if json.Unmarshal([]byte(old.Scope), &r) != nil || r.ID != req.ResourceID || old.RequestedBy != uint32(c.GetUint("admin_id")) {
				return nil, errConflict
			}
			return unchangedMutation{old}, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		rows, err := s.cleanupResources(tx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.ID != req.ResourceID {
				continue
			}
			if r.Reason != "" {
				return nil, publicationError("IMAGE_PROTECTED", r.Reason)
			}
			raw, _ := json.Marshal(r)
			now := time.Now().UTC()
			row := ImageCleanup{ID: req.ID, Scope: string(raw), ScopeHash: control.Digest(raw), Status: "pending", RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
			return row, tx.Create(&row).Error
		}
		return nil, errConflict
	})
}
func (s *Server) approveCleanup(c *gin.Context) {
	s.change(c, nil, "images.cleanup.approve", c.Param("id"), func(tx *gorm.DB) (any, error) {
		var row ImageCleanup
		if tx.First(&row, "id = ?", c.Param("id")).Error != nil {
			return nil, errConflict
		}
		if row.ApprovedBy == uint32(c.GetUint("admin_id")) && row.ApprovedBy != 0 && row.Status != "pending" && row.Status != "rejected" && row.Status != "expired" {
			return unchangedMutation{row}, nil
		}
		if row.Status != "pending" || row.RequestedBy == uint32(c.GetUint("admin_id")) || time.Now().After(row.ExpiresAt) || !s.publicationActor(tx, row.RequestedBy, "ops.write") {
			return nil, errConflict
		}
		var resource CleanupResource
		if json.Unmarshal([]byte(row.Scope), &resource) != nil {
			return nil, errConflict
		}
		rows, err := s.cleanupResources(tx)
		if err != nil {
			return nil, err
		}
		match := false
		for _, r := range rows {
			raw, _ := json.Marshal(r)
			if r.ID == resource.ID && r.Reason == "" && control.Digest(raw) == row.ScopeHash {
				match = true
			}
		}
		if !match {
			return nil, publicationError("CLEANUP_PREVIEW_CHANGED", "References or resource identity changed; create a new preview.")
		}
		row.ApprovedBy = uint32(c.GetUint("admin_id"))
		if resource.Kind == "central" {
			row.Status = "queued"
			return row, tx.Save(&row).Error
		}
		var svc ServiceRecord
		if tx.First(&svc, "id = ?", resource.Service).Error != nil || svc.BusyTask != "" {
			return nil, errConflict
		}
		id := "clean-" + control.Digest([]byte(row.ID))[:48]
		cmd := control.Command{Version: 1, ID: id, HostID: resource.HostID, Service: resource.Service, Action: "image-cleanup", Image: resource.Image, Cleanup: &control.Cleanup{Kind: resource.Kind, SHA256: resource.SHA256, ImageID: resource.ImageID, Platform: resource.Platform}, ExpiresAt: row.ExpiresAt}
		if cmd.Validate() != nil {
			return nil, errConflict
		}
		raw, _ := json.Marshal(cmd)
		task := Task{ID: id, HostID: resource.HostID, ServiceID: resource.Service, Action: cmd.Action, Payload: string(raw), PayloadHash: control.Digest(raw), Status: "queued", RequestedBy: row.RequestedBy, ApprovedBy: row.ApprovedBy, CreatedAt: time.Now().UTC(), ExpiresAt: row.ExpiresAt}
		if err := tx.Create(&task).Error; err != nil {
			return nil, err
		}
		if err := tx.Model(&svc).Update("busy_task", id).Error; err != nil {
			return nil, err
		}
		row.TaskID = id
		row.Status = "queued"
		return row, tx.Save(&row).Error
	})
}
func (s *Server) cleanupList(c *gin.Context) {
	rows := []ImageCleanup{}
	if s.db.Order("created_at DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) rejectCleanup(c *gin.Context) {
	s.change(c, nil, "images.cleanup.reject", c.Param("id"), func(tx *gorm.DB) (any, error) {
		var row ImageCleanup
		if tx.First(&row, "id = ?", c.Param("id")).Error != nil || row.Status != "pending" {
			return nil, errConflict
		}
		row.Status = "rejected"
		return row, tx.Save(&row).Error
	})
}
func (s *Server) recheckCleanup(tx *gorm.DB, t Task) error {
	if t.Action != "image-cleanup" {
		return nil
	}
	var p ImageCleanup
	if tx.First(&p, "task_id = ?", t.ID).Error != nil {
		return errConflict
	}
	var original CleanupResource
	if json.Unmarshal([]byte(p.Scope), &original) != nil {
		return errConflict
	}
	rows, err := s.cleanupResources(tx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.ID == original.ID && r.Reason == "" {
			return nil
		}
	}
	return errConflict
}
func (s *Server) centralCleanupStep() {
	if s.distribution == nil {
		return
	}
	cfg := s.distribution
	f, err := os.OpenFile(filepath.Join(cfg.Directory, "distribution.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		if err := distributionLock(tx); err != nil {
			return err
		}
		var p ImageCleanup
		if tx.Where("status = ? AND task_id = ?", "queued", "").Order("created_at").First(&p).Error != nil {
			return nil
		}
		var r CleanupResource
		if json.Unmarshal([]byte(p.Scope), &r) != nil {
			return errConflict
		}
		rows, err := s.cleanupResources(tx)
		if err != nil {
			return err
		}
		allowed := false
		for _, current := range rows {
			allowed = allowed || (current.ID == r.ID && current.Reason == "")
		}
		if !allowed || time.Now().After(p.ExpiresAt) || !s.publicationActor(tx, p.RequestedBy, "ops.write") || !s.publicationActor(tx, p.ApprovedBy, "ops.approve") {
			p.Status = "failed"
			p.Code = "CLEANUP_PROTECTED"
			return tx.Save(&p).Error
		}
		image := imagecontract.ReleaseImage{Role: r.Service, Reference: r.Image, Platform: r.Platform, ImageID: r.ImageID}
		if _, err := registrycache.RemoveDownloadCache(cfg.Directory, image, false); err != nil {
			p.Status = "failed"
			p.Code = "CACHE_INDEX_UNKNOWN"
			return tx.Save(&p).Error
		}
		if !control.ImageIdentity("sha256:" + r.SHA256) {
			return errConflict
		}
		if err := os.Remove(filepath.Join(cfg.Directory, r.SHA256)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := tx.Delete(&ImageCache{}, "id = ?", r.CacheID).Error; err != nil {
			return err
		}
		p.Status = "succeeded"
		p.Code = "IMAGE_CLEANED"
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		return s.appendAudit(tx, 0, "images.cleanup.result", p.ID, map[string]string{"status": p.Status})
	})
}
