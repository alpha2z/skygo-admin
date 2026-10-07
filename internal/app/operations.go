package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/confirmation"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/scott4game/skygo/cluster"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"strconv"
	"strings"
	"time"
)

var errConflict = errors.New("operation rejected: check identity, state and dependencies")

func read(c *gin.Context, out any) ([]byte, bool) {
	b, err := io.ReadAll(c.Request.Body)
	if err != nil || json.Unmarshal(b, out) != nil {
		c.JSON(400, gin.H{"error": "invalid request"})
		return nil, false
	}
	return b, true
}

// change preserves a durable intent before the mutation. An interrupted email
// confirmation is never replayed as a fresh external operation.
type unchangedMutation struct{ Value any }

func (s *Server) change(c *gin.Context, body []byte, action, target string, fn func(*gorm.DB) (any, error)) {
	admin := uint32(c.GetUint("admin_id"))
	id := c.GetHeader("X-Confirmation-ID")
	var service *confirmation.Service
	if !s.cfg.SkipEmailConfirmation {
		var user AdminUser
		if s.db.First(&user, admin).Error != nil {
			c.Status(401)
			return
		}
		service = &confirmation.Service{DB: s.db, Secret: []byte(s.cfg.JWTSecret), Sender: s.emailSender}
		digest := confirmation.Digest(c.Request.Method, c.Request.URL.Path, body)
		if id == "" {
			challenge, err := service.Issue(c.Request.Context(), admin, user.Email, c.ClientIP(), digest, action+" / "+target)
			if err != nil {
				c.JSON(429, gin.H{"error": "confirmation unavailable or rate limited"})
				return
			}
			c.JSON(428, gin.H{"confirmation_id": challenge.ID, "error": "email confirmation required"})
			return
		}
		row, replay, err := service.Begin(c.Request.Context(), admin, id, c.GetHeader("X-Confirmation-Code"), digest)
		if err != nil {
			c.JSON(409, gin.H{"error": "confirmation invalid or execution needs reconciliation"})
			return
		}
		if replay {
			c.Data(row.ResponseCode, "application/json", []byte(row.Response))
			return
		}
	}
	var result any
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var lock AuditLock
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error; err != nil {
			return err
		}
		var err error
		result, err = fn(tx)
		if err != nil {
			return err
		}
		if unchanged, ok := result.(unchangedMutation); ok {
			result = unchanged.Value
			return nil
		}
		detail := map[string]any{"confirmation": !s.cfg.SkipEmailConfirmation}
		switch row := result.(type) {
		case Task:
			detail["single_confirmation"] = row.SingleConfirmation
		case Workflow:
			detail["single_confirmation"] = row.SingleConfirmation
		case Publication:
			detail["single_confirmation"] = row.SingleConfirmation
		case ImageCleanup:
			detail["single_confirmation"] = row.SingleConfirmation
		}
		return s.appendAudit(tx, admin, action, target, detail)
	})
	status := 200
	if err != nil {
		status = 409
		result = gin.H{"error": errConflict.Error()}
		var safe *releaseError
		if errors.As(err, &safe) {
			result = gin.H{"error": safe.Message, "error_code": safe.Code}
		}
	}
	b, _ := json.Marshal(result)
	if service != nil {
		if service.Finish(c.Request.Context(), id, status, string(b)) != nil {
			c.JSON(503, gin.H{"error": "result requires reconciliation; do not retry mutation"})
			return
		}
	}
	c.Data(status, "application/json", b)
}
func (s *Server) hosts(c *gin.Context) {
	var rows []Host
	if s.db.Order("id").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) addHost(c *gin.Context) {
	var req struct {
		ID string `json:"id"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if !control.Identifier.MatchString(req.ID) {
		c.Status(400)
		return
	}
	s.change(c, body, "host.create", req.ID, func(tx *gorm.DB) (any, error) {
		token, err := randomToken(32)
		if err != nil {
			return nil, err
		}
		h := Host{ID: req.ID, TokenHash: control.Digest([]byte(token)), Active: true, LastSeen: time.Unix(0, 0).UTC()}
		if err = tx.Create(&h).Error; err != nil {
			return nil, err
		}
		return gin.H{"id": req.ID, "token": token, "signing_public_key": base64.StdEncoding.EncodeToString(s.cfg.SigningKey.Public().(ed25519.PublicKey))}, nil
	})
}
func (s *Server) revokeHost(c *gin.Context) {
	s.change(c, nil, "host.revoke", c.Param("id"), func(tx *gorm.DB) (any, error) {
		r := tx.Model(&Host{}).Where("id = ?", c.Param("id")).Update("active", false)
		if r.Error != nil || r.RowsAffected != 1 {
			return nil, errConflict
		}
		return gin.H{"ok": true}, nil
	})
}
func (s *Server) services(c *gin.Context) {
	var rows []ServiceRecord
	if s.db.Order("id").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) saveService(c *gin.Context) {
	var req control.Service
	body, ok := read(c, &req)
	if !ok {
		return
	}
	s.change(c, body, "service.save", req.ID, func(tx *gorm.DB) (any, error) {
		// Lock the common inventory before validating the dependency graph.
		var lock AuditLock
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error != nil {
			return nil, errConflict
		}
		var host Host
		if tx.First(&host, "id = ? AND active = ?", req.HostID, true).Error != nil {
			return nil, errConflict
		}
		var rows []ServiceRecord
		if tx.Find(&rows).Error != nil {
			return nil, errConflict
		}
		all := []control.Service{req}
		for _, row := range rows {
			if row.ID == req.ID {
				if row.BusyTask != "" || workflowLocked(tx, req.ID, "") {
					return nil, errConflict
				}
				continue
			}
			var def control.Service
			if json.Unmarshal([]byte(row.Definition), &def) != nil {
				return nil, errConflict
			}
			all = append(all, def)
		}
		if control.ValidateServices(all) != nil {
			return nil, errConflict
		}
		b, _ := json.Marshal(req)
		row := ServiceRecord{ID: req.ID, Definition: string(b)}
		if err := tx.Save(&row).Error; err != nil {
			return nil, err
		}
		return row, nil
	})
}
func (s *Server) tasks(c *gin.Context) {
	var rows []Task
	if s.db.Order("created_at DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	allowed, _ := s.auth.Enforce(c.GetString("admin_role"), "ops.logs")
	if !allowed {
		for i := range rows {
			var result control.Result
			if json.Unmarshal([]byte(rows[i].Result), &result) == nil {
				result.Logs = ""
				raw, _ := json.Marshal(result)
				rows[i].Result = string(raw)
			}
		}
	}
	c.JSON(200, rows)
}
func (s *Server) createTask(c *gin.Context) {
	var req struct {
		RequestID     string          `json:"request_id"`
		Extension     string          `json:"extension"`
		Payload       json.RawMessage `json:"payload"`
		Service       string          `json:"service"`
		Action        string          `json:"action"`
		Image         string          `json:"image"`
		ConfigVersion string          `json:"config_version"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if req.Action == "prepare-image" || req.Action == "image-cleanup" {
		c.Status(400)
		return
	}
	if req.Action == "configure" {
		allowed, _ := s.auth.Enforce(c.GetString("admin_role"), "config.write")
		if !allowed {
			c.Status(403)
			return
		}
	}
	if req.Action == "logs" {
		allowed, _ := s.auth.Enforce(c.GetString("admin_role"), "ops.logs")
		if !allowed {
			c.Status(403)
			return
		}
	}
	if req.Action == "extension" {
		if req.Image != "" || req.ConfigVersion != "" {
			c.Status(400)
			return
		}
		x, exists := s.cfg.AgentActions[req.Extension]
		allowed := false
		if exists {
			allowed, _ = s.auth.Enforce(c.GetString("admin_role"), x.Permission)
		}
		if !exists || !allowed || !control.Identifier.MatchString(req.RequestID) || x.Validate(req.Payload) != nil {
			c.Status(403)
			return
		}
	} else if req.Extension != "" || len(req.Payload) > 0 {
		c.Status(400)
		return
	}
	if req.RequestID != "" && !control.Identifier.MatchString(req.RequestID) {
		c.Status(400)
		return
	}
	s.change(c, body, "task.create", req.Service, func(tx *gorm.DB) (any, error) {
		if req.RequestID != "" {
			var old Task
			err := tx.First(&old, "id = ?", req.RequestID).Error
			if err == nil {
				var previous control.Command
				if json.Unmarshal([]byte(old.Payload), &previous) != nil || old.RequestedBy != uint32(c.GetUint("admin_id")) || previous.Service != req.Service || previous.Action != req.Action || previous.Image != req.Image || previous.Extension != req.Extension || old.ConfigVersion != req.ConfigVersion || !bytes.Equal(previous.Payload, req.Payload) {
					return nil, errConflict
				}
				return unchangedMutation{old}, nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
		var record ServiceRecord
		if tx.First(&record, "id = ?", req.Service).Error != nil {
			return nil, errConflict
		}
		var def control.Service
		if json.Unmarshal([]byte(record.Definition), &def) != nil {
			return nil, errConflict
		}
		id, err := randomToken(24)
		if err != nil {
			return nil, err
		}
		if req.RequestID != "" {
			id = req.RequestID
		}
		cmd := control.Command{Version: 1, ID: id, HostID: def.HostID, Service: def.ID, Action: req.Action, Image: req.Image, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		var pluginHost Host
		if tx.First(&pluginHost, "id = ?", def.HostID).Error == nil {
			var inventory []control.Observation
			if json.Unmarshal([]byte(pluginHost.Observations), &inventory) == nil {
				for _, o := range inventory {
					if o.Service == def.ID {
						if o.ObservationOnly && req.Action != "health" {
							return nil, errConflict
						}
						cmd.PluginRevision = o.PluginRevision
						break
					}
				}
			}
		}
		if req.Action == "extension" {
			cmd.Extension = req.Extension
			cmd.Payload = req.Payload
			if err := s.checkExtensionTask(tx, cmd, uint32(c.GetUint("admin_id")), 0); err != nil {
				return nil, err
			}

		}

		if req.Action == "configure" {
			var config ConfigRelease
			if tx.First(&config, "id = ? AND service_id = ?", req.ConfigVersion, req.Service).Error != nil {
				return nil, errConflict
			}
			cmd.Config = json.RawMessage(config.Content)
			cmd.ConfigHash = config.SHA256
		} else if req.ConfigVersion != "" {
			return nil, errConflict
		}
		if req.Action == "deploy" || req.Action == "rollback" {
			var host Host
			var observations []control.Observation
			if tx.First(&host, "id = ?", def.HostID).Error != nil || json.Unmarshal([]byte(host.Observations), &observations) != nil {
				return nil, errConflict
			}
			for _, o := range observations {
				if o.Service == def.ID && o.SyncImageEnv {
					if !hasCapability(o, "image.env.v1") {
						return nil, errConflict
					}
					cmd.SyncImageEnv = true
					cmd.InventoryRevision = o.InventoryRevision
					cmd.PreviousImageID = o.ImageID
				}
			}
		}
		if s.cfg.TaskPolicy != nil {
			if err := s.cfg.TaskPolicy(c.Request.Context(), tx, cmd); err != nil {
				return nil, err
			}
		}
		if cmd.Validate() != nil {
			return nil, errConflict
		}
		b, _ := json.Marshal(cmd)
		row := Task{SingleConfirmation: !s.cfg.IndependentApprovalEnabled, ID: id, HostID: def.HostID, ServiceID: def.ID, Action: req.Action, Payload: string(b), PayloadHash: control.Digest(b), Status: "pending", RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: time.Now().UTC(), ExpiresAt: cmd.ExpiresAt, ConfigVersion: req.ConfigVersion}
		if err = tx.Create(&row).Error; err != nil {
			return nil, err
		}
		if row.SingleConfirmation {
			return s.authorizeTask(c, tx, row.ID)
		}
		return row, nil
	})
}
func (s *Server) approveTask(c *gin.Context) {
	s.change(c, nil, "task.approve", c.Param("id"), func(tx *gorm.DB) (any, error) {
		return s.authorizeTask(c, tx, c.Param("id"))
	})
}
func (s *Server) authorizeTask(c *gin.Context, tx *gorm.DB, id string) (any, error) {
	var lock AuditLock
	if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error != nil {
		return nil, errConflict
	}
	var units int64
	if tx.Model(&Publication{}).Where("status IN ?", []string{"queued", "dispatched", "uncertain"}).Count(&units).Error != nil || units > 0 {
		return nil, publicationError("PUBLICATION_BLOCKING", "An active publication holds the control-plane execution gate.")
	}

	var task Task
	if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id = ?", id).Error != nil || task.Status != "pending" || (!task.SingleConfirmation && task.RequestedBy == uint32(c.GetUint("admin_id"))) || !time.Now().Before(task.ExpiresAt) {
		return nil, errConflict
	}
	if err := s.taskAuthority(tx, task); err != nil {
		return nil, err
	}
	var record ServiceRecord
	if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, "id = ?", task.ServiceID).Error != nil || record.BusyTask != "" || workflowLocked(tx, task.ServiceID, "") {
		return nil, errConflict
	}
	var def control.Service
	if json.Unmarshal([]byte(record.Definition), &def) != nil || def.HostID != task.HostID {
		return nil, errConflict
	}
	var host Host
	if tx.First(&host, "id = ?", def.HostID).Error != nil || !host.Active || time.Since(host.LastSeen) > time.Minute {
		return nil, errConflict
	}
	var payload control.Command
	if json.Unmarshal([]byte(task.Payload), &payload) != nil {
		return nil, errConflict
	}
	if s.cfg.TaskPolicy != nil {
		if err := s.cfg.TaskPolicy(c.Request.Context(), tx, payload); err != nil {
			return nil, err
		}
	}
	if err := s.checkExtensionTask(tx, payload, task.RequestedBy, approvalIdentity(task.SingleConfirmation, uint32(c.GetUint("admin_id")))); err != nil {
		return nil, err
	}
	if payload.SyncImageEnv {
		var observations []control.Observation
		json.Unmarshal([]byte(host.Observations), &observations)
		ok := false
		for _, o := range observations {
			ok = ok || (o.Service == task.ServiceID && o.SyncImageEnv && o.InventoryRevision == payload.InventoryRevision && o.ImageID == payload.PreviousImageID)
		}
		if !ok {
			return nil, errConflict
		}
	}
	if task.Action == "start" || task.Action == "restart" || task.Action == "deploy" || task.Action == "rollback" {
		for _, dependency := range def.DependsOn {
			if !s.healthy(tx, dependency) {
				return nil, errConflict
			}
		}
	}
	if task.Action == "stop" {
		var services []ServiceRecord
		tx.Find(&services)
		for _, r := range services {
			var d control.Service
			json.Unmarshal([]byte(r.Definition), &d)
			for _, dep := range d.DependsOn {
				if dep == def.ID && s.healthy(tx, d.ID) {
					return nil, errConflict
				}
			}
		}
	}
	if def.ControlPlane {
		var count int64
		if tx.Model(&Task{}).Where("status IN ?", []string{"queued", "dispatched", "uncertain"}).Count(&count).Error != nil || count > 0 {
			return nil, errConflict
		}
	}
	record.BusyTask = task.ID
	if tx.Save(&record).Error != nil {
		return nil, errConflict
	}
	task.Status = "queued"
	task.ApprovedBy = uint32(c.GetUint("admin_id"))
	if tx.Save(&task).Error != nil {
		return nil, errConflict
	}
	return task, nil
}

func (s *Server) healthy(tx *gorm.DB, id string) bool {
	var r ServiceRecord
	if tx.First(&r, "id = ?", id).Error != nil {
		return false
	}
	var d control.Service
	if json.Unmarshal([]byte(r.Definition), &d) != nil {
		return false
	}
	var h Host
	if tx.First(&h, "id = ? AND active = ?", d.HostID, true).Error != nil || time.Since(h.LastSeen) > time.Minute {
		return false
	}
	var observations []control.Observation
	if json.Unmarshal([]byte(h.Observations), &observations) != nil {
		return false
	}
	for _, o := range observations {
		if o.Service == id {
			return o.Healthy
		}
	}
	return false
}
func (s *Server) rejectTask(c *gin.Context) {
	s.change(c, nil, "task.reject", c.Param("id"), func(tx *gorm.DB) (any, error) {
		r := tx.Model(&Task{}).Where("id = ? AND status = ?", c.Param("id"), "pending").Update("status", "rejected")
		if r.Error != nil || r.RowsAffected != 1 {
			return nil, errConflict
		}
		return gin.H{"ok": true}, nil
	})
}
func (s *Server) configs(c *gin.Context) {
	var rows []ConfigRelease
	if s.db.Order("created_at DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) createConfig(c *gin.Context) {
	var req struct {
		Service string          `json:"service"`
		Kind    string          `json:"kind"`
		Content json.RawMessage `json:"content"`
	}
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if len(req.Content) == 0 || len(req.Content) > 256<<10 || !json.Valid(req.Content) || (req.Kind != "json" && req.Kind != "skygo") {
		c.Status(400)
		return
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(req.Content))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		c.Status(400)
		return
	}
	req.Content, _ = json.Marshal(decoded)
	if req.Kind == "skygo" {
		var nodes []cluster.Endpoint
		if json.Unmarshal(req.Content, &nodes) != nil || len(nodes) > 200 {
			c.Status(400)
			return
		}
		snapshot, err := cluster.NewStaticRegistry(nodes...).Load(context.Background())
		if err != nil {
			c.Status(400)
			return
		}
		req.Content, _ = json.Marshal(snapshot)
	}
	s.change(c, body, "config.create", req.Service, func(tx *gorm.DB) (any, error) {
		var r ServiceRecord
		if tx.First(&r, "id = ?", req.Service).Error != nil {
			return nil, errConflict
		}
		id, err := randomToken(24)
		if err != nil {
			return nil, err
		}
		row := ConfigRelease{ID: id, ServiceID: req.Service, Content: string(req.Content), SHA256: control.Digest(req.Content), Kind: req.Kind, RequestedBy: uint32(c.GetUint("admin_id")), CreatedAt: time.Now().UTC()}
		if err = tx.Create(&row).Error; err != nil {
			return nil, err
		}
		return row, nil
	})
}
func (s *Server) audit(c *gin.Context) {
	var rows []Audit
	if s.db.Order("audit_id DESC").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) admins(c *gin.Context) {
	var rows []AdminUser
	if s.db.Select("id", "username", "email", "role", "active").Limit(200).Find(&rows).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, rows)
}
func (s *Server) createAdmin(c *gin.Context) {
	var req struct{ Username, Password, Email, Role string }
	body, ok := read(c, &req)
	if !ok {
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 64 || len(req.Password) < 12 || len(req.Password) > 1024 || !confirmation.ValidEmail(req.Email) || (req.Role != "superadmin" && req.Role != "operator" && req.Role != "viewer" && req.Role != "approver") {
		c.Status(400)
		return
	}
	s.change(c, body, "admin.create", req.Username, func(tx *gorm.DB) (any, error) {
		hash, err := hashSecret(req.Password)
		if err != nil {
			return nil, err
		}
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "Skygo Admin", AccountName: req.Username})
		if err != nil {
			return nil, err
		}
		u := AdminUser{Username: req.Username, Email: req.Email, PasswordHash: hash, TOTPSecret: key.Secret(), Role: req.Role, Active: true}
		if err = tx.Create(&u).Error; err != nil {
			return nil, err
		}
		permissions := []string{"ops.read", "config.read", "build.read"}
		if req.Role == "operator" {
			permissions = append(permissions, "ops.write", "ops.logs", "config.write", "build.write")
		}
		if req.Role == "approver" {
			permissions = append(permissions, "ops.approve", "audit.read")
		}
		if req.Role == "superadmin" {
			permissions = allPermissions
		}
		for _, p := range permissions {
			if tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RolePolicy{Role: req.Role, Permission: p}).Error != nil {
				return nil, errConflict
			}
		}
		return gin.H{"id": u.ID, "totp_uri": key.URL()}, nil
	})
	// Reload after the transaction; no permission grants for an uncommitted user.
	var policies []RolePolicy
	if s.db.Find(&policies).Error == nil {
		for _, p := range policies {
			s.auth.AddPolicy(p.Role, p.Permission)
			s.auth.AddGroupingPolicy(p.Role, p.Role)
		}
	}
}
func (s *Server) disableAdmin(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == uint64(c.GetUint("admin_id")) {
		c.Status(400)
		return
	}
	s.change(c, nil, "admin.disable", c.Param("id"), func(tx *gorm.DB) (any, error) {
		r := tx.Model(&AdminUser{}).Where("id = ?", id).Update("active", false)
		if r.Error != nil || r.RowsAffected != 1 {
			return nil, errConflict
		}
		if tx.Delete(&Session{}, "admin_id = ?", id).Error != nil {
			return nil, errConflict
		}
		return gin.H{"ok": true}, nil
	})
}
func (s *Server) agentAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Host-ID")
		raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		var h Host
		if !control.Identifier.MatchString(id) || len(raw) < 32 || len(raw) > 256 || s.db.First(&h, "id = ?", id).Error != nil || !h.Active || subtle.ConstantTimeCompare([]byte(h.TokenHash), []byte(control.Digest([]byte(raw)))) != 1 {
			c.AbortWithStatus(401)
			return
		}
		c.Set("host_id", id)
		c.Next()
	}
}
func (s *Server) heartbeat(c *gin.Context) {
	var req control.Heartbeat
	if _, ok := read(c, &req); !ok {
		return
	}
	if req.Version != 1 || !control.Identifier.MatchString(req.BootID) || len(req.Observations) > 200 {
		c.Status(400)
		return
	}
	b, _ := json.Marshal(req.Observations)
	if s.db.Model(&Host{}).Where("id = ?", c.GetString("host_id")).Updates(map[string]any{"boot_id": req.BootID, "last_seen": time.Now().UTC(), "observations": string(b)}).Error != nil {
		c.Status(503)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) commands(c *gin.Context) {
	envelopes := []control.Envelope{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var lock AuditLock
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error; err != nil {
			return err
		}
		var tasks []Task
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("host_id = ? AND status IN ?", c.GetString("host_id"), []string{"queued", "dispatched", "uncertain"}).Order("created_at").Limit(20).Find(&tasks).Error != nil {
			return errConflict
		}
		for _, t := range tasks {
			var command control.Command
			if json.Unmarshal([]byte(t.Payload), &command) != nil {
				return errConflict
			}
			if t.PublicationID != "" && t.Status == "queued" {
				var p Publication
				if tx.First(&p, "id = ?", t.PublicationID).Error != nil {
					return errConflict
				}
				err := s.recheckPublication(tx, p)
				if err != nil || time.Now().After(t.ExpiresAt) {
					code := "PUBLICATION_EXPIRED"
					if e, ok := err.(*releaseError); ok {
						code = e.Code
					} else if err != nil {
						code = "SCOPE_INVALID"
					}
					t.Status = "failed"
					if tx.Save(&t).Error != nil {
						return errConflict
					}
					if err := s.publicationState(tx, t, "failed", code); err != nil {
						return err
					}
					if err := s.appendAudit(tx, 0, "publication.dispatch.rejected", p.ID, map[string]string{"code": code}); err != nil {
						return err
					}
					continue
				}
			}
			if time.Now().After(t.ExpiresAt) && t.Status == "queued" {
				t.Status = "expired"
				if tx.Save(&t).Error != nil {
					return errConflict
				}
				if tx.Model(&ServiceRecord{}).Where("id = ? AND busy_task = ?", t.ServiceID, t.ID).Update("busy_task", "").Error != nil {
					return errConflict
				}
				continue
			}
			if t.Status == "queued" && s.recheckCleanup(tx, t) != nil {
				t.Status = "failed"
				if tx.Save(&t).Error != nil {
					return errConflict
				}
				if tx.Model(&ServiceRecord{}).Where("busy_task = ?", t.ID).Update("busy_task", "").Error != nil {
					return errConflict
				}
				if tx.Model(&ImageCleanup{}).Where("task_id = ?", t.ID).Updates(map[string]any{"status": "failed", "code": "CLEANUP_PROTECTED"}).Error != nil {
					return errConflict
				}
				continue
			}
			if t.Status == "queued" {
				policyErr := s.checkExtensionTask(tx, command, t.RequestedBy, approvalIdentity(t.SingleConfirmation, t.ApprovedBy))
				if policyErr == nil {
					policyErr = s.taskAuthority(tx, t)
				}
				if policyErr == nil && t.WorkflowID != "" {
					policyErr = s.workflowDispatch(tx, t)
				}
				code := "EXTENSION_AUTHORIZATION_CHANGED"
				if policyErr == nil && s.cfg.TaskPolicy != nil {
					policyErr = s.cfg.TaskPolicy(c.Request.Context(), tx, command)
					code = "COMPOSITION_POLICY_REJECTED"
				}
				if policyErr != nil {
					t.Status = "failed"
					now := time.Now().UTC()
					t.FinishedAt = &now
					result, _ := json.Marshal(control.Result{ID: t.ID, Status: "failed", Code: code})
					t.Result = string(result)
					if tx.Save(&t).Error != nil || tx.Model(&ServiceRecord{}).Where("busy_task = ?", t.ID).Update("busy_task", "").Error != nil {
						return errConflict
					}
					if s.appendAudit(tx, 0, "task.policy.rejected", t.ID, nil) != nil {
						return errConflict
					}
					continue
				}
			}
			envelope, err := control.Sign(command, s.cfg.SigningKey)
			if err != nil {
				return err
			}
			if t.Status == "queued" {
				t.Status = "dispatched"
				if tx.Save(&t).Error != nil {
					return errConflict
				}
			}
			if err := s.publicationState(tx, t, t.Status, ""); err != nil {
				return err
			}
			envelopes = append(envelopes, envelope)
		}
		return nil
	})
	if err != nil {
		c.Status(503)
		return
	}
	c.JSON(200, envelopes)
}
func (s *Server) result(c *gin.Context) {
	var req control.Result
	if _, ok := read(c, &req); !ok {
		return
	}
	if len(req.Data) > 64<<10 || (len(req.Data) > 0 && !json.Valid(req.Data)) || !control.Identifier.MatchString(req.ID) || (req.Status != "succeeded" && req.Status != "failed" && req.Status != "uncertain" && req.Status != "rolled_back") || len(req.Code) > 160 || len(req.Logs) > 16384 {
		c.Status(400)
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var lock AuditLock
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error != nil {
			return errConflict
		}
		var t Task
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&t, "id = ? AND host_id = ?", req.ID, c.GetString("host_id")).Error != nil {
			return errConflict
		}
		raw, _ := json.Marshal(req)
		if t.Status == "succeeded" || t.Status == "failed" || t.Status == "rolled_back" {
			if t.Result == string(raw) {
				return nil
			}
			return errConflict
		}
		if t.Status != "dispatched" && t.Status != "uncertain" {
			return errConflict
		}
		var command control.Command
		if json.Unmarshal([]byte(t.Payload), &command) != nil {
			return errConflict
		}
		if t.Action == "control-unit" {
			if t.PublicationID == "" || !validUnitResult(command, req) {
				return errConflict
			}
		} else if req.Status == "rolled_back" || len(req.Unit) > 0 {
			return errConflict
		}
		if req.Status == "succeeded" {
			if (t.Action == "deploy" || t.Action == "rollback") && req.Image != command.Image {
				return errConflict
			}
			if t.Action == "configure" && req.ConfigHash != command.ConfigHash {
				return errConflict
			}
		}
		if req.Status == "succeeded" && t.Action == "image-cleanup" {
			if command.Cleanup == nil || req.Code != "IMAGE_CLEANED" || req.Image != command.Image || req.ImageID != command.Cleanup.ImageID || req.Platform != command.Cleanup.Platform {
				return errConflict
			}
		}
		if req.Status == "succeeded" && t.Action == "prepare-image" {
			if req.Image != command.Image || req.Platform != command.Platform || !validRuntimeImageID(req.ImageID) {
				return errConflict
			}
		}
		now := time.Now().UTC()
		t.FinishedAt = &now
		t.Status = req.Status
		t.Result = string(raw)
		if tx.Save(&t).Error != nil {
			return errConflict
		}
		if req.Status != "uncertain" {
			if tx.Model(&ServiceRecord{}).Where("id = ? AND busy_task = ?", t.ServiceID, t.ID).Update("busy_task", "").Error != nil {
				return errConflict
			}
		}
		if req.Status == "succeeded" && t.Action == "configure" {
			if tx.Model(&ConfigRelease{}).Where("service_id = ?", t.ServiceID).Update("active", false).Error != nil {
				return errConflict
			}
			if tx.Model(&ConfigRelease{}).Where("id = ?", t.ConfigVersion).Update("active", true).Error != nil {
				return errConflict
			}
		}
		if t.Action == "image-cleanup" {
			if req.Status == "succeeded" && command.Cleanup != nil && command.Cleanup.Kind == "image" {
				pid := preparationID(t.HostID, prepareTarget{Service: t.ServiceID, Platform: command.Cleanup.Platform, Image: command.Image})
				if err := tx.Delete(&ImagePreparation{}, "id = ?", pid).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&ImageCleanup{}).Where("task_id = ?", t.ID).Updates(map[string]any{"status": req.Status, "code": req.Code}).Error; err != nil {
				return err
			}
		}
		if err := s.finishDelivery(tx, t, req); err != nil {
			return err
		}
		if err := s.publicationState(tx, t, t.Status, req.Code); err != nil {
			return err
		}
		return s.appendAudit(tx, 0, "task.result", t.ID, map[string]string{"status": req.Status, "code": req.Code})
	})
	if err != nil {
		c.Status(409)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// taskDetail preserves access to durable results after they leave the recent list.
func (s *Server) taskDetail(c *gin.Context) {
	var row Task
	if !control.Identifier.MatchString(c.Param("id")) {
		c.JSON(400, gin.H{"error": "invalid task ID"})
		return
	}
	err := s.db.Where("id = ?", c.Param("id")).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "task not found"})
		return
	}
	if err != nil {
		c.JSON(503, gin.H{"error": "task unavailable"})
		return
	}
	allowed, _ := s.auth.Enforce(c.GetString("admin_role"), "ops.logs")
	if !allowed {
		var result control.Result
		if json.Unmarshal([]byte(row.Result), &result) == nil {
			result.Logs = ""
			raw, _ := json.Marshal(result)
			row.Result = string(raw)
		} else {
			row.Result = "{}"
		}
	}
	c.JSON(200, row)
}
