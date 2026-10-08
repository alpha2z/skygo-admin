package app

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/plugin"
	"github.com/gin-gonic/gin"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDataPanelResourceBoundary(t *testing.T) {
	endpoint := plugin.Endpoint{ID: "example", Revision: strings.Repeat("a", 64), Socket: filepath.Join(t.TempDir(), "data.sock")}
	listener, err := net.Listen("unix", endpoint.Socket)
	if err != nil {
		t.Fatal(err)
	}
	cat := plugin.DataCatalog{Version: 1, Datasets: []plugin.DataSet{{ID: "queue", Title: "Queue", Unit: "items", Modes: []string{"current"}}}, Panels: []plugin.DataPanel{{ID: "overview", Title: "Overview", Widgets: []plugin.DataWidget{{Dataset: "queue", Kind: "card"}}}}}
	server := &http.Server{Handler: plugin.Handler(endpoint, func(ctx context.Context, r plugin.Request) (any, error) {
		if r.Resource.Actor.Permission != "ops.read" || r.Resource.Actor.ID != 7 {
			t.Error("identity not forwarded")
		}
		var body any = cat
		if r.Resource.Path != "/data/catalog" {
			zero := 0.0
			now := time.Now()
			body = plugin.DataResult{Version: 1, State: "ok", SampledAt: &now, Warnings: []string{}, Values: []plugin.DataValue{{Value: &zero}}}
		}
		raw, _ := json.Marshal(body)
		return plugin.ResourceResponse{Status: 200, Body: raw}, nil
	})}
	go server.Serve(listener)
	defer server.Close()
	x := Extension{ID: "example", Policies: []RolePolicy{{Role: "superadmin", Permission: "ops.read"}}}
	files := map[string][]byte{}
	x.Assets = plugin.AssetFS(files)
	p := PluginProviderConfig{Endpoint: endpoint, DataPanels: []PluginDataPanelConfig{{ID: "overview", Title: "Data", Permission: "ops.read"}}}
	if err := installDataPanels(&x, p, files); err != nil {
		t.Fatal(err)
	}
	if err := validateExtensions([]Extension{x}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin_id", uint(7)); c.Set("admin_role", "viewer") })
	for _, r := range x.Routes {
		router.Handle(r.Method, r.Path, r.Handler)
	}
	for path, status := range map[string]int{"/data/catalog": 200, "/data/datasets/queue/current": 200, "/data/datasets/secret/current": 404, "/data/datasets/queue/current?sql=x": 400, "/data/datasets/queue/series": 400} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	endpoint.Revision = strings.Repeat("b", 64)
	var out any
	if endpoint.Call(context.Background(), plugin.Request{Operation: "resource"}, &out) == nil {
		t.Fatal("revision mismatch accepted")
	}
}
func TestDataPanelsRequireInstallationGrant(t *testing.T) {
	f := pluginPageFixture(t)
	var cfg PluginFile
	raw, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &cfg)
	cfg.Providers[0].DataPanels = []PluginDataPanelConfig{{ID: "monitoring", Title: "Data", Permission: "ops.read"}}
	raw, _ = json.Marshal(cfg)
	os.WriteFile(f, raw, 0600)
	t.Setenv("ADMIN_PLUGINS_CONFIG", f)
	if loadPluginActions(&Config{}) == nil {
		t.Fatal("undeclared capability granted")
	}
}

func TestDataPanelReadAuthorizationMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	fixture := pluginPageFixture(t)
	var installation PluginFile
	raw, _ := os.ReadFile(fixture)
	json.Unmarshal(raw, &installation)
	// No data-read capability: installation fails before any routes can be enabled.
	installation.Providers[0].DataPanels = []PluginDataPanelConfig{{ID: "overview", Title: "Data", Permission: "ops.read"}}
	raw, _ = json.Marshal(installation)
	os.WriteFile(fixture, raw, 0600)
	t.Setenv("ADMIN_PLUGINS_CONFIG", fixture)
	if loadPluginActions(&cfg) == nil {
		t.Fatal("untrusted data capability accepted")
	}
	// Test the same core authorization middleware used by installed data routes.
	cfg.Extensions = []Extension{{ID: "sample", runtimePlugin: true, Policies: []RolePolicy{{Role: "superadmin", Permission: "ops.read"}}, Routes: []ExtensionRoute{{Method: "GET", Path: "/data/catalog", Permission: "ops.read", Handler: func(c *gin.Context) { c.JSON(200, gin.H{"version": 1}) }}}}}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	router := s.Router()
	check(t, call(router, nil, "GET", "/api/v1/plugins/sample/data/catalog", nil, nil), 401)
	password := "synthetic-data-test-password"
	check(t, call(router, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, router, "owner", password)
	check(t, call(router, owner, "GET", "/api/v1/plugins/sample/data/catalog", nil, nil), 200)
	if err := db.Where("role = ? AND permission = ?", "superadmin", "ops.read").Delete(&RolePolicy{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.RemovePolicy("superadmin", "ops.read"); err != nil {
		t.Fatal(err)
	}
	check(t, call(router, owner, "GET", "/api/v1/plugins/sample/data/catalog", nil, nil), 403)
}

// TestInstalledDataProviderContract can verify any trusted fixture against the
// same compiled core routes without introducing provider-specific dependencies.
func TestInstalledDataProviderContract(t *testing.T) {
	file := os.Getenv("DATA_PROVIDER_INSTALLATION")
	if file == "" {
		t.Skip("external fixture not configured")
	}
	t.Setenv("ADMIN_PLUGINS_CONFIG", file)
	cfg := Config{}
	if err := loadPluginActions(&cfg); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin_id", uint(7)); c.Set("admin_role", "viewer") })
	for _, x := range cfg.Extensions {
		for _, route := range x.Routes {
			router.Handle(route.Method, "/"+x.ID+route.Path, route.Handler)
		}
	}
	request := func(path string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	for _, x := range cfg.Extensions {
		var cat plugin.DataCatalog
		if err := plugin.DecodeData(request("/"+x.ID+"/data/catalog"), &cat); err != nil {
			t.Fatal(err)
		}
		if len(cat.Datasets) == 0 {
			t.Fatal("no installed data")
		}
		for _, d := range cat.Datasets {
			for _, mode := range d.Modes {
				path := "/" + x.ID + "/data/datasets/" + d.ID + "/" + mode
				if mode == "series" {
					end := time.Now().UTC().Truncate(time.Second)
					path += "?start=" + end.Add(-time.Minute).Format(time.RFC3339) + "&end=" + end.Format(time.RFC3339) + "&step=15"
				}
				var result plugin.DataResult
				if err := plugin.DecodeData(request(path), &result); err != nil {
					t.Fatal(err)
				}
				if err := result.Validate(d, mode); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestDataRequestCancellationReachesProvider(t *testing.T) {
	dir, err := os.MkdirTemp("", "dpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	endpoint := plugin.Endpoint{ID: "example", Revision: strings.Repeat("a", 64), Socket: filepath.Join(dir, "cancel.sock")}
	listener, err := net.Listen("unix", endpoint.Socket)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	server := &http.Server{Handler: plugin.Handler(endpoint, func(ctx context.Context, r plugin.Request) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})}
	go server.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- endpoint.Call(ctx, plugin.Request{Operation: "resource"}, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider not called")
	}
	cancel()
	if <-done == nil {
		t.Fatal("cancelled request succeeded")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("provider context not cancelled")
	}
}
