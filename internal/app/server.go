package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/confirmation"
	"github.com/alpha2z/skygo-admin/internal/githubbuild"
	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

var allPermissions = []string{"ops.read", "ops.write", "ops.approve", "ops.logs", "host.manage", "config.read", "config.write", "audit.read", "admin.manage", "build.read", "build.write"}

const casbinModel = "[request_definition]\nr = sub, obj\n[policy_definition]\np = sub, obj\n[role_definition]\ng = _, _\n[policy_effect]\ne = some(where (p.eft == allow))\n[matchers]\nm = g(r.sub, p.sub) && r.obj == p.obj"

type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}
type Server struct {
	maintenanceCancel context.CancelFunc
	maintenanceDone   chan struct{}
	autoDone          chan struct{}
	cfg               Config
	db                *gorm.DB
	auth              *casbin.SyncedEnforcer
	sessionOK         func(context.Context, uint32, string) bool
	emailSender       confirmation.Sender
	github            githubBuildClient
	httpServer        *http.Server
	listener          net.Listener
}

func OpenDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("admin database unavailable")
	}
	sql, err := db.DB()
	if err != nil {
		return nil, err
	}
	sql.SetMaxOpenConns(16)
	sql.SetMaxIdleConns(4)
	sql.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}
func NewServer(cfg Config, db *gorm.DB) (*Server, error) {
	var version SchemaVersion
	if db.First(&version, 1).Error != nil || version.Version != 3 {
		return nil, errors.New("run admin-api -migrate before starting")
	}
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, db: db, auth: enforcer}
	s.sessionOK = s.activeAdminSession
	var policies []RolePolicy
	if err = db.Find(&policies).Error; err != nil {
		return nil, err
	}
	for _, p := range policies {
		s.auth.AddPolicy(p.Role, p.Permission)
		s.auth.AddGroupingPolicy(p.Role, p.Role)
	}
	if !cfg.SkipEmailConfirmation {
		s.emailSender = confirmation.SMTP{Address: cfg.SMTPAddress, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom}
	}
	if cfg.GitHubConfig != "" {
		b, err := os.ReadFile(cfg.GitHubConfig)
		if err != nil {
			return nil, errors.New("GitHub configuration unavailable")
		}
		var c githubbuild.Config
		if json.Unmarshal(b, &c) != nil {
			return nil, errors.New("invalid GitHub configuration")
		}
		s.github, err = githubbuild.New(c)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Server) Start(context.Context) error {
	l, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return errors.New("admin listener unavailable")
	}
	s.listener = l
	s.httpServer = &http.Server{Handler: s.Router(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go s.httpServer.Serve(l)
	ctx, cancel := context.WithCancel(context.Background())
	s.maintenanceCancel = cancel
	s.maintenanceDone = make(chan struct{})
	s.autoDone = make(chan struct{})
	go func() { defer close(s.autoDone); s.autoRegisterLoop(ctx) }()
	go func() {
		defer close(s.maintenanceDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			work, done := context.WithTimeout(ctx, 5*time.Second)
			_ = s.expireTasks(work)
			done()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
func (s *Server) Stop(ctx context.Context) error {
	if s.maintenanceCancel != nil {
		s.maintenanceCancel()
		select {
		case <-s.maintenanceDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.autoDone != nil {
		select {
		case <-s.autoDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}
func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	if err := r.SetTrustedProxies(s.cfg.TrustedProxyCIDRs); err != nil {
		panic("invalid trusted proxy configuration")
	}
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) { c.AbortWithStatusJSON(500, gin.H{"error": "internal error"}) }), securityHeaders(), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512<<10)
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) {
		sql, err := s.db.DB()
		if err != nil || sql.PingContext(c.Request.Context()) != nil {
			c.Status(503)
			return
		}
		c.JSON(200, gin.H{"ok": true})
	})
	r.GET("/api/v1/bootstrap/status", s.bootstrapStatus)
	r.POST("/api/v1/bootstrap", s.bootstrap)
	r.GET("/api/v1/auth/captcha", s.issueLoginCaptcha)
	r.POST("/api/v1/login", s.login)
	r.GET("/api/v1/auth/settings", func(c *gin.Context) {
		c.JSON(200, gin.H{"totp_enabled": s.cfg.TOTPEnabled, "email_confirmation_enabled": !s.cfg.SkipEmailConfirmation, "captcha_required": true})
	})
	a := r.Group("/api/v1", s.authenticate(), s.csrf())
	a.GET("/session", func(c *gin.Context) {
		policies, _ := s.auth.GetPermissionsForUser(c.GetString("admin_role"))
		permissions := []string{}
		for _, p := range policies {
			if len(p) > 1 {
				permissions = append(permissions, p[1])
			}
		}
		c.JSON(200, gin.H{"id": c.GetUint("admin_id"), "role": c.GetString("admin_role"), "permissions": permissions})
	})
	a.POST("/logout", s.logout)
	a.GET("/hosts", s.require("ops.read"), s.hosts)
	a.POST("/hosts", s.require("host.manage"), s.addHost)
	a.POST("/hosts/:id/revoke", s.require("host.manage"), s.revokeHost)
	a.GET("/services", s.require("ops.read"), s.services)
	a.POST("/services", s.require("ops.write"), s.saveService)
	a.GET("/tasks", s.require("ops.read"), s.tasks)
	a.POST("/tasks", s.require("ops.write"), s.createTask)
	a.POST("/tasks/:id/approve", s.require("ops.approve"), s.approveTask)
	a.POST("/tasks/:id/reject", s.require("ops.approve"), s.rejectTask)
	a.GET("/configs", s.require("config.read"), s.configs)
	a.POST("/configs", s.require("config.write"), s.createConfig)
	a.GET("/audit", s.require("audit.read"), s.audit)
	a.GET("/admins", s.require("admin.manage"), s.admins)
	a.POST("/admins", s.require("admin.manage"), s.createAdmin)
	a.POST("/admins/:id/disable", s.require("admin.manage"), s.disableAdmin)
	a.GET("/release-settings", s.require("build.read"), s.releaseSettings)
	a.POST("/release-settings/keys", s.require("admin.manage"), s.require("build.write"), s.addBuildKey)
	a.GET("/releases", s.require("build.read"), s.releaseList)
	a.GET("/releases/:id", s.require("build.read"), s.releaseDetail)
	a.POST("/builds/:id/register", s.require("build.write"), s.registerBuild)
	a.GET("/releases/:id/admin-preparation", s.require("ops.read"), s.require("build.read"), s.adminPreparation)
	a.POST("/releases/:id/prepare-admin", s.require("ops.write"), s.require("build.read"), s.prepareAdminImages)
	a.GET("/publication-candidates", s.require("ops.read"), s.require("build.read"), s.publicationCandidates)
	a.POST("/publication-preparation", s.require("ops.write"), s.require("build.read"), s.prepareSelection)
	a.GET("/publications", s.require("ops.read"), s.publications)
	a.GET("/publications/:id", s.require("ops.read"), s.publicationDetail)
	a.POST("/publications", s.require("ops.write"), s.require("build.read"), s.createPublication)
	a.POST("/publications/:id/approve", s.require("ops.approve"), s.approvePublication)
	a.POST("/publications/:id/reject", s.require("ops.approve"), s.rejectPublication)
	a.GET("/builds", s.require("build.read"), s.builds)
	a.POST("/builds", s.require("build.write"), s.dispatchBuild)
	agents := r.Group("/agent/v1", s.agentAuth())
	agents.POST("/heartbeat", s.heartbeat)
	agents.GET("/commands", s.commands)
	agents.POST("/results", s.result)
	r.StaticFile("/", s.cfg.WebRoot+"/index.html")
	r.StaticFile("/publications.js", s.cfg.WebRoot+"/publications.js")
	r.StaticFile("/releases.js", s.cfg.WebRoot+"/releases.js")
	r.StaticFile("/app.js", s.cfg.WebRoot+"/app.js")
	r.StaticFile("/styles.css", s.cfg.WebRoot+"/styles.css")
	return r
}
