package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/imagecontract"
	"github.com/alpha2z/skygo-admin/internal/registrycache"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type distributionConfig struct {
	Enabled   bool     `json:"enabled"`
	Directory string   `json:"directory"`
	MaxBytes  int64    `json:"max_bytes"`
	Username  string   `json:"username"`
	TokenFile string   `json:"token_file"`
	Packages  []string `json:"packages"`
}
type ImageDelivery struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	TaskID    string    `gorm:"size:64;index" json:"task_id"`
	HostID    string    `gorm:"size:64;index" json:"host_id"`
	Service   string    `gorm:"size:64" json:"service"`
	ReleaseID string    `gorm:"size:64" json:"release_id"`
	Image     string    `gorm:"size:512" json:"image"`
	Platform  string    `gorm:"size:32" json:"platform"`
	Mode      string    `gorm:"size:16" json:"mode"`
	Status    string    `gorm:"size:32;index" json:"status"`
	CacheID   string    `gorm:"size:64" json:"cache_id,omitempty"`
	Attempt   uint64    `json:"attempt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type ImageAttempt struct {
	DeliveryID string     `gorm:"primaryKey;size:64" json:"delivery_id"`
	Number     uint64     `gorm:"primaryKey" json:"number"`
	Sequence   uint64     `json:"sequence"`
	Phase      string     `gorm:"size:32" json:"phase"`
	Status     string     `gorm:"size:32" json:"status"`
	Bytes      int64      `json:"bytes"`
	Total      int64      `json:"total"`
	Reused     int64      `json:"reused"`
	Downloaded *int64     `json:"downloaded,omitempty"`
	Reason     string     `gorm:"size:64" json:"reason,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
type ImageCache struct {
	ID             string     `gorm:"primaryKey;size:64" json:"id"`
	Image          string     `gorm:"size:512" json:"image"`
	Service        string     `gorm:"size:64" json:"service"`
	Platform       string     `gorm:"size:32" json:"platform"`
	ImageID        string     `gorm:"size:80" json:"image_id"`
	SHA256         string     `gorm:"size:64" json:"sha256"`
	Size           int64      `json:"size"`
	BuildStartedAt *time.Time `json:"build_started_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (s *Server) loadDistribution() error {
	if s.cfg.DistributionConfig == "" {
		return nil
	}
	b, err := os.ReadFile(s.cfg.DistributionConfig)
	if err != nil {
		return errors.New("distribution configuration unavailable")
	}
	var cfg distributionConfig
	if json.Unmarshal(b, &cfg) != nil {
		return errors.New("invalid distribution configuration")
	}
	if !cfg.Enabled {
		return nil
	}
	if !filepath.IsAbs(cfg.Directory) || !filepath.IsAbs(cfg.TokenFile) || cfg.Username == "" || len(cfg.Packages) == 0 {
		return errors.New("distribution requires local cache and approved registry packages")
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 64 << 30
	}
	if cfg.MaxBytes < 8<<30 || cfg.MaxBytes > 64<<30 {
		return errors.New("distribution cache quota must be 8 to 64 GiB")
	}
	if os.MkdirAll(cfg.Directory, 0700) != nil {
		return errors.New("distribution cache unavailable")
	}
	s.distribution = &cfg
	return nil
}
func (s *Server) queuePreparation(tx *gorm.DB, t *Task, releaseID string, h Host) *gorm.DB {
	var c control.Command
	if json.Unmarshal([]byte(t.Payload), &c) != nil {
		return &gorm.DB{Error: errConflict}
	}
	d := ImageDelivery{ID: t.ID, TaskID: t.ID, HostID: h.ID, Service: t.ServiceID, ReleaseID: releaseID, Image: c.Image, Platform: c.Platform, Mode: "direct", Status: "queued", CreatedAt: time.Now().UTC()}
	if s.distribution != nil {
		var observations []control.Observation
		json.Unmarshal([]byte(h.Observations), &observations)
		capable := false
		for _, o := range observations {
			if o.Service == t.ServiceID {
				for _, cap := range o.Capabilities {
					capable = capable || cap == "image.archive.v1"
				}
			}
		}
		if !capable {
			return &gorm.DB{Error: publicationError("ARCHIVE_CAPABILITY_REQUIRED", "Upgrade the selected Agent for archive distribution.")}
		}
		approved := false
		for _, repo := range s.distribution.Packages {
			approved = approved || strings.HasPrefix(c.Image, "ghcr.io/"+repo+"@")
		}
		if !approved {
			return &gorm.DB{Error: publicationError("REGISTRY_NOT_APPROVED", "Image repository is not approved for central distribution.")}
		}
		d.Mode = "archive"
		d.Status = "preparing"
		t.Status = "preparing"
	}
	c.DeliveryID = d.ID
	raw, _ := json.Marshal(c)
	t.Payload = string(raw)
	t.PayloadHash = control.Digest(raw)
	t.DeliveryID = d.ID
	if err := tx.Create(&d).Error; err != nil {
		return &gorm.DB{Error: err}
	}
	return tx.Create(t)
}
func distributionLock(tx *gorm.DB) error {
	var lock AuditLock
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error
}
func newAttempt(tx *gorm.DB, d *ImageDelivery, phase string) error {
	now := time.Now().UTC()
	if err := tx.Model(&ImageAttempt{}).Where("delivery_id = ? AND finished_at IS NULL", d.ID).Updates(map[string]any{"status": "interrupted", "finished_at": now}).Error; err != nil {
		return err
	}
	d.Attempt++
	a := ImageAttempt{DeliveryID: d.ID, Number: d.Attempt, Status: "working", Phase: phase, StartedAt: now}
	if err := tx.Create(&a).Error; err != nil {
		return err
	}
	return tx.Save(d).Error
}
func (s *Server) distributionLoop(ctx context.Context) {
	if s.distribution == nil {
		return
	}
	for {
		s.distributionStep(ctx)
		s.centralCleanupStep()
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (s *Server) distributionStep(ctx context.Context) {
	cfg := s.distribution
	if cfg == nil {
		return
	}
	// Cross-process exclusion protects shared blobs against concurrent cleanup.
	f, err := os.OpenFile(filepath.Join(cfg.Directory, "distribution.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	var d ImageDelivery
	var task Task
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := distributionLock(tx); err != nil {
			return err
		}
		if err := tx.Where("mode = ? AND status = ?", "archive", "preparing").Order("created_at").First(&d).Error; err != nil {
			return err
		}
		if err := tx.First(&task, "id = ?", d.TaskID).Error; err != nil {
			return err
		}
		if _, err := s.trustedRelease(tx, d.ReleaseID); err != nil {
			return err
		}
		if time.Now().After(task.ExpiresAt) {
			return errConflict
		}
		return newAttempt(tx, &d, "registry-download")
	})
	if err != nil {
		return
	}
	var used int64
	err = filepath.WalkDir(cfg.Directory, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return errConflict
		}
		if !e.IsDir() {
			info, err := e.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		return nil
	})
	var result registrycache.Archive
	if err == nil && used+(8<<30) > cfg.MaxBytes {
		err = imagecontract.Fail("CACHE_QUOTA_EXCEEDED")
	}
	if err == nil {
		client := &registrycache.Client{MaxCacheBytes: cfg.MaxBytes, Username: cfg.Username, TokenFile: cfg.TokenFile, Packages: cfg.Packages}
		if s.registryClient != nil {
			client = s.registryClient
		}
		image := imagecontract.ReleaseImage{Role: serviceKind(s.db, d.Service), Reference: d.Image, Platform: d.Platform}
		work, cancel := context.WithTimeout(ctx, 30*time.Minute)
		var reused, processed int64
		var sequence uint64
		report := func(bytes int64, reason string) {
			if bytes > processed {
				processed = bytes
			}
			downloaded := processed - reused
			if downloaded < 0 {
				downloaded = 0
			}
			sequence++
			_ = s.storeProgress(s.db, d, control.Progress{Attempt: d.Attempt, Sequence: sequence, Phase: "registry-download", Bytes: processed, Reused: reused, Downloaded: &downloaded, Reason: reason})
		}
		result, err = client.Download(work, image, cfg.Directory, func(n int64) { report(n, "") }, func(n int64, reason string) { reused = n; report(n, reason) })
		cancel()
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		if e := distributionLock(tx); e != nil {
			return e
		}
		var current ImageDelivery
		if tx.First(&current, "id = ?", d.ID).Error != nil || current.TaskID != task.ID || current.Attempt != d.Attempt {
			return errConflict
		}
		if tx.First(&task, "id = ?", task.ID).Error != nil {
			return errConflict
		}
		if current.Status != "preparing" || task.Status != "preparing" || time.Now().After(task.ExpiresAt) {
			return nil
		}
		if ctx.Err() != nil {
			now := time.Now().UTC()
			return tx.Model(&ImageAttempt{}).Where("delivery_id = ? AND number = ?", d.ID, d.Attempt).Updates(map[string]any{"status": "interrupted", "finished_at": now, "reason": "CONTROLLER_RESTART"}).Error
		}
		now := time.Now().UTC()
		code := ""
		status := "ready"
		if err != nil {
			status = "failed"
			code = imagecontract.FailureCode(err)
		}
		if e := tx.Model(&ImageAttempt{}).Where("delivery_id = ? AND number = ?", d.ID, d.Attempt).Updates(map[string]any{"status": status, "finished_at": now, "reason": code}).Error; e != nil {
			return e
		}
		if err != nil {
			current.Status = "failed"
			task.Status = "failed"
		} else {
			id := imagecontract.ReleaseImage{Role: serviceKind(tx, d.Service), Reference: d.Image, Platform: d.Platform}.Key()
			cache := ImageCache{ID: id, Service: serviceKind(tx, d.Service), Image: d.Image, Platform: d.Platform, ImageID: result.ImageID, SHA256: result.SHA256, Size: result.Size, CreatedAt: now}
			if timestamp, e := time.Parse(time.RFC3339Nano, result.BuildStartedAt); e == nil {
				cache.BuildStartedAt = &timestamp
			}
			if e := tx.Save(&cache).Error; e != nil {
				return e
			}
			current.CacheID = id
			current.Status = "queued"
			task.Status = "queued"
			var c control.Command
			if json.Unmarshal([]byte(task.Payload), &c) != nil {
				return errConflict
			}
			c.Archive = &control.Archive{SHA256: result.SHA256, Size: result.Size, ImageID: result.ImageID}
			raw, _ := json.Marshal(c)
			task.Payload = string(raw)
			task.PayloadHash = control.Digest(raw)
		}
		if e := tx.Save(&task).Error; e != nil {
			return e
		}
		return tx.Save(&current).Error
	})
}
func serviceKind(tx *gorm.DB, id string) string {
	var r ServiceRecord
	var d control.Service
	if tx.First(&r, "id = ?", id).Error == nil {
		json.Unmarshal([]byte(r.Definition), &d)
	}
	if d.ControlKind != "" {
		return d.ControlKind
	}
	return id
}
func (s *Server) storeProgress(db *gorm.DB, d ImageDelivery, p control.Progress) error {
	if p.Bytes < 0 || p.Reused < 0 || p.Reused > p.Bytes || p.Total < 0 || (p.Total > 0 && p.Bytes > p.Total) || p.Sequence == 0 || p.Attempt != d.Attempt || len(p.Phase) > 32 || len(p.Reason) > 64 || (p.Downloaded != nil && *p.Downloaded < 0) {
		return errConflict
	}
	updates := map[string]any{"sequence": p.Sequence, "phase": p.Phase, "bytes": p.Bytes, "total": p.Total, "reused": p.Reused}
	if p.Reason != "" {
		updates["reason"] = p.Reason
	}
	if p.Downloaded != nil {
		updates["downloaded"] = *p.Downloaded
	}
	r := db.Model(&ImageAttempt{}).Where("delivery_id = ? AND number = ? AND sequence < ? AND bytes <= ? AND reused <= ? AND finished_at IS NULL", d.ID, p.Attempt, p.Sequence, p.Bytes, p.Reused).Updates(updates)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return errConflict
	}
	return nil
}
func (s *Server) beginDeliveryAttempt(c *gin.Context) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if _, ok := read(c, &req); !ok {
		return
	}
	var d ImageDelivery
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := distributionLock(tx); err != nil {
			return err
		}
		var t Task
		if tx.First(&d, "id = ? AND host_id = ? AND task_id = ?", c.Param("id"), c.GetString("host_id"), req.TaskID).Error != nil || tx.First(&t, "id = ?", d.TaskID).Error != nil || (t.Status != "dispatched" && t.Status != "uncertain") || time.Now().After(t.ExpiresAt) {
			return errConflict
		}
		d.Status = "working"
		return newAttempt(tx, &d, "agent-prepare")
	})
	if err != nil {
		c.Status(409)
		return
	}
	c.JSON(200, gin.H{"attempt": d.Attempt})
}
func (s *Server) deliveryProgress(c *gin.Context) {
	var p control.Progress
	if _, ok := read(c, &p); !ok {
		return
	}
	var d ImageDelivery
	if s.db.First(&d, "id = ? AND host_id = ? AND task_id = ?", c.Param("id"), c.GetString("host_id"), p.TaskID).Error != nil || s.storeProgress(s.db, d, p) != nil {
		c.Status(409)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) deliveryArchive(c *gin.Context) {
	var d ImageDelivery
	var t Task
	var cache ImageCache
	if s.distribution == nil || s.db.First(&d, "id = ? AND host_id = ?", c.Param("id"), c.GetString("host_id")).Error != nil || s.db.First(&t, "id = ?", d.TaskID).Error != nil || t.Status != "dispatched" || time.Now().After(t.ExpiresAt) || s.db.First(&cache, "id = ?", d.CacheID).Error != nil {
		c.Status(403)
		return
	}
	if !control.ImageIdentity("sha256:"+cache.SHA256) || cache.Size <= 0 || cache.Size > 8<<30 {
		c.Status(409)
		return
	}
	f, err := os.OpenFile(filepath.Join(s.distribution.Directory, cache.SHA256), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		c.Status(404)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != cache.Size {
		c.Status(409)
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
	c.Header("ETag", "\""+cache.SHA256+"\"")
	http.ServeContent(c.Writer, c.Request, cache.SHA256, info.ModTime(), f)
}
func (s *Server) imageDeliveries(c *gin.Context) {
	rows := []ImageDelivery{}
	if s.db.Order("created_at DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) imageDelivery(c *gin.Context) {
	var row ImageDelivery
	if s.db.First(&row, "id = ?", c.Param("id")).Error != nil {
		c.Status(404)
		return
	}
	c.JSON(200, row)
}
func (s *Server) imageAttempts(c *gin.Context) {
	rows := []ImageAttempt{}
	if s.db.Where("delivery_id = ?", c.Param("id")).Order("number DESC").Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, gin.H{"attempts": rows, "recorded": len(rows) > 0})
}
func (s *Server) retryDelivery(c *gin.Context) {
	s.change(c, nil, "images.retry", c.Param("id"), func(tx *gorm.DB) (any, error) {
		var d ImageDelivery
		var old Task
		var h Host
		if tx.First(&d, "id = ?", c.Param("id")).Error != nil || tx.First(&old, "id = ?", d.TaskID).Error != nil {
			return nil, errConflict
		}
		if old.Status == "queued" || old.Status == "dispatched" || old.Status == "preparing" {
			return unchangedMutation{d}, nil
		}
		if old.Status == "uncertain" {
			return nil, errConflict
		}
		if _, err := s.trustedRelease(tx, d.ReleaseID); err != nil {
			return nil, err
		}
		if tx.First(&h, "id = ?", d.HostID).Error != nil || !h.Active || time.Since(h.LastSeen) > time.Minute {
			return nil, errConflict
		}
		var service ServiceRecord
		if tx.First(&service, "id = ?", d.Service).Error != nil || service.BusyTask != "" {
			return nil, errConflict
		}
		id, err := randomToken(24)
		if err != nil {
			return nil, err
		}
		var cmd control.Command
		if json.Unmarshal([]byte(old.Payload), &cmd) != nil {
			return nil, errConflict
		}
		cmd.ID = id
		cmd.ExpiresAt = time.Now().UTC().Add(time.Hour)
		cmd.Archive = nil
		raw, _ := json.Marshal(cmd)
		task := Task{ID: id, DeliveryID: d.ID, HostID: d.HostID, ServiceID: d.Service, Action: "prepare-image", Payload: string(raw), PayloadHash: control.Digest(raw), Status: "queued", RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: time.Now().UTC(), ExpiresAt: cmd.ExpiresAt}
		if d.Mode == "archive" {
			if s.distribution == nil {
				return nil, errConflict
			}
			task.Status = "preparing"
		}
		d.TaskID = id
		d.Status = task.Status
		if err := tx.Create(&task).Error; err != nil {
			return nil, err
		}
		if err := tx.Save(&ImagePreparation{ID: preparationID(d.HostID, prepareTarget{Service: d.Service, Platform: d.Platform, Image: d.Image}), TaskID: id, HostBootID: h.BootID}).Error; err != nil {
			return nil, err
		}
		return d, tx.Save(&d).Error
	})
}
func (s *Server) finishDelivery(tx *gorm.DB, t Task, r control.Result) error {
	if t.DeliveryID == "" {
		return nil
	}
	now := time.Now().UTC()
	if err := tx.Model(&ImageAttempt{}).Where("delivery_id = ? AND finished_at IS NULL", t.DeliveryID).Updates(map[string]any{"status": r.Status, "phase": "finished", "reason": r.Code, "finished_at": now}).Error; err != nil {
		return err
	}
	return tx.Model(&ImageDelivery{}).Where("id = ? AND task_id = ?", t.DeliveryID, t.ID).Update("status", r.Status).Error
}
