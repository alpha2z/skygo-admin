package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/alpha2z/skygo-admin/internal/confirmation"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Extension is statically installed by a trusted composition executable. Its
// routes are always below /api/v1/extensions/<ID>; core routes cannot be replaced.
type Extension struct {
	runtimePlugin bool
	PublicRoutes  []ExtensionRoute
	ID            string
	Policies      []RolePolicy
	Routes        []ExtensionRoute
	Pages         []ExtensionPage
	Assets        fs.FS
	Start         func(context.Context) error
	Stop          func(context.Context) error
}
type ExtensionRoute struct {
	// Draft accepts streaming input but may not activate a release or execute a service.
	Draft      bool
	BodyLimit  int64
	Method     string
	Path       string
	Permission string
	Handler    gin.HandlerFunc
}
type ExtensionPage struct {
	Group      string   `json:"group,omitempty"`
	Replaces   []string `json:"replaces,omitempty"`
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Permission string   `json:"permission"`
	Script     string   `json:"script"`
}

var extensionID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var permissionID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func validateExtensions(xs []Extension) error {
	seen := map[string]bool{}
	for _, x := range xs {
		if !extensionID.MatchString(x.ID) || seen[x.ID] {
			return errors.New("invalid extension identity")
		}
		seen[x.ID] = true
		permissions := map[string]bool{}
		for _, p := range x.Policies {
			if !permissionID.MatchString(p.Permission) || (p.Role != "superadmin" && p.Role != "operator" && p.Role != "approver" && p.Role != "viewer") {
				return errors.New("invalid extension policy")
			}
			permissions[p.Permission] = true
		}
		routes := map[string]bool{}
		for _, r := range x.Routes {
			if r.BodyLimit < 0 || r.BodyLimit > 8<<30 || (r.BodyLimit > 1<<20 && !r.Draft) || (r.Draft && r.Method != "POST") || r.Handler == nil || !permissions[r.Permission] || !strings.HasPrefix(r.Path, "/") || strings.ContainsAny(r.Path, "?#\\") || strings.Contains(r.Path, "..") || path.Clean(r.Path) != r.Path {
				return errors.New("invalid extension route")
			}
			if r.Method != "GET" && r.Method != "POST" && r.Method != "PUT" && r.Method != "PATCH" && r.Method != "DELETE" {
				return errors.New("invalid extension method")
			}
			key := r.Method + " " + r.Path
			if routes[key] {
				return errors.New("duplicate extension route")
			}
			routes[key] = true
		}
		for _, r := range x.PublicRoutes {
			first := strings.Split(strings.TrimPrefix(r.Path, "/"), "/")[0]
			if !extensionID.MatchString(first) || first == "api" || first == "agent" || first == "extensions" || first == "healthz" || (r.Method != "GET" && r.Method != "HEAD") || r.Handler == nil || !strings.HasPrefix(r.Path, "/") || strings.Contains(r.Path, "..") || strings.ContainsAny(r.Path, "?#\\") {
				return errors.New("invalid public extension route")
			}
		}
		pages := map[string]bool{}
		for _, p := range x.Pages {
			if !extensionID.MatchString(p.ID) || pages[p.ID] || p.Title == "" || !permissions[p.Permission] || x.Assets == nil || !fs.ValidPath(p.Script) || !strings.HasSuffix(p.Script, ".js") {
				return errors.New("invalid extension page")
			}
			if _, err := fs.Stat(x.Assets, p.Script); err != nil {
				return errors.New("missing extension script")
			}
			for _, name := range p.Replaces {
				switch name {
				case "services", "tasks", "builds", "publications", "system-update", "distribution":
				default:
					return errors.New("invalid navigation replacement")
				}
			}
			pages[p.ID] = true
		}
	}
	return nil
}

// ExtensionPolicies is run explicitly during composition database initialization,
// never during normal startup. Existing policy choices are not overwritten.
func ExtensionPolicies(db *gorm.DB, xs []Extension) error {
	if err := validateExtensions(xs); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, x := range xs {
			for _, p := range x.Policies {
				var n int64
				if err := tx.Model(&RolePolicy{}).Where("role = ? AND permission = ?", p.Role, p.Permission).Count(&n).Error; err != nil {
					return err
				}
				if n == 0 {
					if err := tx.Create(&p).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
func (s *Server) Allowed(role, permission string) (bool, error) {
	return s.auth.Enforce(role, permission)
}
func (s *Server) SessionActive(ctx context.Context, id uint32, role string) bool {
	return s.activeAdminSession(ctx, id, role)
}
func (s *Server) AppendAudit(tx *gorm.DB, id uint32, action, target string, detail any) error {
	return s.appendAudit(tx, id, action, target, detail)
}

// ConfirmedHandler uses the same durable confirmation store as core mutations.
// An interrupted handler remains executing and cannot be automatically replayed.
func (s *Server) ConfirmedHandler(action string, handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(413)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		var service *confirmation.Service
		id := c.GetHeader("X-Confirmation-ID")
		if !s.cfg.SkipEmailConfirmation {
			var user AdminUser
			if s.db.First(&user, uint32(c.GetUint("admin_id"))).Error != nil {
				c.Status(401)
				return
			}
			service = &confirmation.Service{DB: s.db, Secret: []byte(s.cfg.JWTSecret), Sender: s.emailSender}
			digest := confirmation.Digest(c.Request.Method, c.Request.URL.RequestURI(), body)
			if id == "" {
				row, err := service.Issue(c.Request.Context(), user.ID, user.Email, c.ClientIP(), digest, action)
				if err != nil {
					c.JSON(429, gin.H{"error": "confirmation unavailable"})
					return
				}
				c.JSON(428, gin.H{"confirmation_id": row.ID})
				return
			}
			row, replay, err := service.Begin(c.Request.Context(), user.ID, id, c.GetHeader("X-Confirmation-Code"), digest)
			if err != nil {
				c.JSON(409, gin.H{"error": "confirmation invalid or reconciliation required"})
				return
			}
			if replay {
				c.Data(row.ResponseCode, "application/json", []byte(row.Response))
				return
			}
		}
		// Persist intent before a callback can reach an external system. Do not log its body.
		if s.db.Transaction(func(tx *gorm.DB) error {
			return s.appendAudit(tx, uint32(c.GetUint("admin_id")), action, c.Request.URL.Path, map[string]bool{"intent": true})
		}) != nil {
			c.Status(503)
			return
		}
		recorder := &extensionResponse{ResponseWriter: c.Writer, status: 200}
		original := c.Writer
		c.Writer = recorder
		defer func() { c.Writer = original }()
		handler(c)
		c.Writer = original
		if recorder.overflow {
			c.JSON(503, gin.H{"error": "extension result requires reconciliation"})
			return
		}
		if service != nil {
			if service.Finish(c.Request.Context(), id, recorder.status, recorder.body.String()) != nil {
				c.JSON(503, gin.H{"error": "result requires reconciliation; do not retry mutation"})
				return
			}
		}
		c.Data(recorder.status, "application/json", recorder.body.Bytes())
	}
}

type extensionResponse struct {
	gin.ResponseWriter
	body     bytes.Buffer
	overflow bool
	status   int
}

func (w *extensionResponse) WriteHeader(code int) { w.status = code }
func (w *extensionResponse) WriteHeaderNow()      {}
func (w *extensionResponse) Status() int          { return w.status }
func (w *extensionResponse) Size() int            { return w.body.Len() }
func (w *extensionResponse) Written() bool        { return w.body.Len() > 0 }
func (w *extensionResponse) Flush()               { w.overflow = true }
func (w *extensionResponse) Write(b []byte) (int, error) {
	if w.body.Len()+len(b) > 1<<20 {
		w.overflow = true
		return 0, errors.New("extension response too large")
	}
	return w.body.Write(b)
}
func (w *extensionResponse) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (s *Server) mountExtensions(r *gin.Engine, a *gin.RouterGroup) {
	a.GET("/extensions", func(c *gin.Context) {
		pages := []ExtensionPage{}
		for _, x := range s.cfg.Extensions {
			for _, p := range x.Pages {
				if ok, _ := s.Allowed(c.GetString("admin_role"), p.Permission); ok {
					p.ID = x.ID + "." + p.ID
					p.Script = "/extensions/" + x.ID + "/assets/" + p.Script
					pages = append(pages, p)
				}
			}
		}
		c.JSON(200, pages)
	})
	for _, x := range s.cfg.Extensions {
		for _, route := range x.PublicRoutes {
			r.Handle(route.Method, route.Path, route.Handler)
		}
		prefix := "/extensions/"
		if x.runtimePlugin {
			prefix = "/plugins/"
		}
		group := a.Group(prefix + x.ID)
		for _, route := range x.Routes {
			h := route.Handler
			if route.Draft {
				original := h
				h = func(c *gin.Context) {
					if s.db.Transaction(func(tx *gorm.DB) error {
						return s.appendAudit(tx, uint32(c.GetUint("admin_id")), "extension.draft", c.Request.URL.Path, nil)
					}) != nil {
						c.Status(503)
						return
					}
					original(c)
				}
			} else if route.Method != http.MethodGet {
				h = s.ConfirmedHandler("extension."+x.ID, h)
			}
			group.Handle(route.Method, route.Path, s.require(route.Permission), h)
		}
		if x.Assets != nil {
			r.Group("/extensions/"+x.ID, func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }, s.authenticate()).StaticFS("/assets", http.FS(x.Assets))
		}
	}
}

// AgentAction is registered only by the composition binary. Execution uses the
// task journal and the operation's persisted approval policy.
type AgentAction struct {
	PluginRevision     string
	Permission         string
	ApprovalPermission string
	Validate           func(json.RawMessage) error
}

func (s *Server) checkExtensionTask(tx *gorm.DB, cmd control.Command, requester, approver uint32) error {
	if cmd.Action != "extension" {
		return nil
	}
	x, ok := s.cfg.AgentActions[cmd.Extension]
	if !ok || x.PluginRevision != cmd.PluginRevision || x.Validate(cmd.Payload) != nil {
		return errConflict
	}
	for _, identity := range []struct {
		id         uint32
		permission string
	}{{requester, x.Permission}, {approver, x.ApprovalPermission}} {
		id, permission := identity.id, identity.permission
		if id == 0 {
			continue
		}
		var u AdminUser
		if tx.First(&u, id).Error != nil || !u.Active {
			return errConflict
		}
		if allowed, _ := s.auth.Enforce(u.Role, permission); !allowed {
			return errConflict
		}
	}
	var h Host
	if tx.First(&h, "id = ?", cmd.HostID).Error != nil || !h.Active {
		return errConflict
	}
	var observations []control.Observation
	if json.Unmarshal([]byte(h.Observations), &observations) != nil {
		return errConflict
	}
	for _, o := range observations {
		if o.Service == cmd.Service && hasCapability(o, "extension."+cmd.Extension+".v1") {
			return nil
		}
	}
	return errConflict
}

func (s *Server) requestBodyLimit(c *gin.Context) int64 {
	for _, x := range s.cfg.Extensions {
		for _, r := range x.Routes {
			if r.BodyLimit > 0 && c.Request.Method == r.Method && c.FullPath() == "/api/v1/extensions/"+x.ID+r.Path {
				return r.BodyLimit
			}
		}
	}
	return 512 << 10
}
