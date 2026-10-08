package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/plugin"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// PluginActionConfig binds a locally approved provider to existing task permissions.
// It does not grant permissions or migrate policy tables during startup.
type PluginActionConfig struct {
	Name               string `json:"name"`
	Permission         string `json:"permission"`
	ApprovalPermission string `json:"approval_permission"`
}
type PluginRouteConfig struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Permission string `json:"permission"`
}

type PluginProviderConfig struct {
	DataPanels []PluginDataPanelConfig `json:"data_panels,omitempty"`
	Workflows  []PluginWorkflowConfig  `json:"workflows,omitempty"`
	Services   []string                `json:"services,omitempty"`
	Routes     []PluginRouteConfig     `json:"routes,omitempty"`
	plugin.Endpoint
	plugin.Trust
	Actions         []PluginActionConfig `json:"actions"`
	AssetsDirectory string               `json:"assets_directory,omitempty"`
	Pages           []ExtensionPage      `json:"pages,omitempty"`
}
type PluginFile struct {
	Version   int                    `json:"version"`
	Providers []PluginProviderConfig `json:"providers"`
}

func loadPluginActions(c *Config) error {
	file := os.Getenv("ADMIN_PLUGINS_CONFIG")
	if file == "" {
		return nil
	}
	var cfg PluginFile
	if err := plugin.ReadConfig(file, &cfg); err != nil {
		return err
	}
	if cfg.Version != plugin.Version || len(cfg.Providers) > 16 {
		return errors.New("unsupported plugin configuration")
	}
	if c.AgentActions == nil {
		c.AgentActions = map[string]AgentAction{}
	}
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		if p.Validate() != nil || seen[p.ID] {
			return errors.New("invalid plugin provider")
		}
		names := []string{}
		if len(p.DataPanels) > 0 {
			names = append(names, "data-read")
		}
		if len(p.Services) > 0 {
			names = append(names, "inventory-read")
		}
		for _, a := range p.Actions {
			names = append(names, a.Name)
		}
		if len(p.Routes) > 0 {
			names = append(names, "resource-api")
		}
		if len(p.Workflows) > 0 {
			names = append(names, "workflow-plan")
		}
		if err := p.Trust.Verify(p.Endpoint, names); err != nil {
			return err
		}
		seen[p.ID] = true
		if c.PluginInventories == nil {
			c.PluginInventories = map[string][]string{}
		}
		scope := map[string]bool{}
		for _, id := range p.Services {
			if !control.Identifier.MatchString(id) || scope[id] {
				return errors.New("invalid plugin inventory scope")
			}
			scope[id] = true
		}
		c.PluginInventories[p.ID] = append([]string(nil), p.Services...)
		if err := installPluginWorkflows(c, p); err != nil {
			return err
		}
		if err := installPluginPages(c, p); err != nil {
			return err
		}
		for _, action := range p.Actions {
			if !permissionID.MatchString(action.Name) || !permissionID.MatchString(action.Permission) || !permissionID.MatchString(action.ApprovalPermission) {
				return errors.New("invalid plugin action policy")
			}
			if _, ok := c.AgentActions[action.Name]; ok {
				return errors.New("duplicate plugin action")
			}
			endpoint := p.Endpoint
			name := action.Name
			c.AgentActions[name] = AgentAction{PluginRevision: endpoint.Revision, Permission: action.Permission, ApprovalPermission: action.ApprovalPermission, Validate: func(raw json.RawMessage) error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return endpoint.Call(ctx, plugin.Request{Operation: "validate", Action: name, Payload: raw}, nil)
			}}
		}
	}
	return nil
}

// installPluginPages reuses the authenticated extension catalog and core permissions.
func installPluginPages(c *Config, p PluginProviderConfig) error {
	if len(p.Pages) == 0 && len(p.Routes) == 0 && len(p.DataPanels) == 0 {
		return nil
	}
	raw, err := os.ReadFile(p.ManifestFile)
	if err != nil {
		return err
	}
	encoded, err := os.ReadFile(p.PublicKeyFile)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return err
	}
	m, manifestRevision, err := plugin.VerifyManifest(raw, key)
	if err != nil {
		return err
	}
	if manifestRevision != p.Revision || m.ID != p.ID {
		return errors.New("plugin manifest changed while loading")
	}
	assets := map[string][]byte{}
	if len(p.Pages) > 0 {
		assets, err = plugin.ReadAssets(p.AssetsDirectory, m)
		if err != nil {
			return err
		}
	}
	allowed := map[string]bool{}
	for _, permission := range allPermissions {
		allowed[permission] = true
	}
	x := Extension{ID: p.ID, Assets: plugin.AssetFS(assets), Pages: p.Pages, runtimePlugin: true}
	for _, page := range p.Pages {
		if !allowed[page.Permission] {
			return errors.New("plugin page requires explicitly provisioned core permission")
		}
		x.Policies = append(x.Policies, RolePolicy{Role: "superadmin", Permission: page.Permission})
	}
	x.Policies = append(x.Policies, RolePolicy{Role: "superadmin", Permission: "ops.read"})
	endpoint := p.Endpoint
	x.Routes = []ExtensionRoute{{Method: "GET", Path: "/health", Permission: "ops.read", Handler: func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		var health struct {
			Ready bool `json:"ready"`
		}
		if endpoint.Call(ctx, plugin.Request{Operation: "health"}, &health) != nil {
			c.JSON(503, gin.H{"id": endpoint.ID, "ready": false, "error": "plugin unavailable"})
			return
		}
		c.JSON(200, gin.H{"id": endpoint.ID, "revision": endpoint.Revision, "ready": health.Ready})
	}}}
	for _, route := range p.Routes {
		if route.Path == "/inventory" && len(p.Services) > 0 {
			return errors.New("plugin inventory route is reserved")
		}
		if !allowed[route.Permission] {
			return errors.New("plugin route requires explicitly provisioned permission")
		}
		x.Policies = append(x.Policies, RolePolicy{Role: "superadmin", Permission: route.Permission})
		route := route
		x.Routes = append(x.Routes, ExtensionRoute{Method: route.Method, Path: route.Path, Permission: route.Permission, BodyLimit: 256 << 10, Handler: func(c *gin.Context) {
			operation := c.GetHeader("X-Operation-ID")
			if c.Request.Method != http.MethodGet && !control.Identifier.MatchString(operation) {
				c.JSON(400, gin.H{"error": "stable operation identity required"})
				return
			}
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, (256<<10)+1))
			if err != nil || len(body) > 256<<10 || len(body) > 0 && !json.Valid(body) {
				c.JSON(400, gin.H{"error": "bounded JSON body required"})
				return
			}
			prefix := "/api/v1/plugins/" + p.ID
			resource := plugin.ResourceRequest{Actor: plugin.Actor{ID: uint32(c.GetUint("admin_id")), Role: c.GetString("admin_role"), Permission: route.Permission}, OperationID: operation, Method: c.Request.Method, Path: strings.TrimPrefix(c.Request.URL.Path, prefix), Query: c.Request.URL.Query(), Body: body}
			var result plugin.ResourceResponse
			if endpoint.Call(c.Request.Context(), plugin.Request{Operation: "resource", Resource: &resource}, &result) != nil {
				c.JSON(503, gin.H{"error": "plugin result unavailable; reconcile before retrying a mutation"})
				return
			}
			if result.Status < 200 || result.Status >= 600 || result.Status >= 300 && result.Status < 400 || !json.Valid(result.Body) {
				c.JSON(502, gin.H{"error": "invalid plugin resource result"})
				return
			}
			c.Data(result.Status, "application/json", result.Body)
		}})
	}
	if err := installDataPanels(&x, p, assets); err != nil {
		return err
	}
	c.Extensions = append(c.Extensions, x)
	return validateExtensions(c.Extensions)
}
