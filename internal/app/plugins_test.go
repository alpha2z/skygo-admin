package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/plugin"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func pluginPageFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	asset := []byte("export function render(ctx){ctx.root.textContent='Example plugin';}")
	sum := sha256.Sum256(asset)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := plugin.Manifest{Protocol: 1, CoreProtocol: 1, ID: "example", Version: "1.0.0", Capabilities: []string{"resource-api"}, Assets: map[string]string{"page.js": hex.EncodeToString(sum[:])}}
	payload, _ := json.Marshal(m)
	signed, _ := json.Marshal(plugin.SignedManifest{Payload: payload, Signature: ed25519.Sign(key, payload)})
	_, revision, err := plugin.VerifyManifest(signed, pub)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", signed)
	write("public.key", []byte(base64.StdEncoding.EncodeToString(pub)))
	write("page.js", asset)
	cfg := PluginFile{Version: 1, Providers: []PluginProviderConfig{{Endpoint: plugin.Endpoint{ID: "example", Socket: filepath.Join(dir, "offline.sock"), Revision: revision}, Trust: plugin.Trust{ManifestFile: filepath.Join(dir, "manifest.json"), PublicKeyFile: filepath.Join(dir, "public.key")}, AssetsDirectory: dir, Pages: []ExtensionPage{{ID: "overview", Title: "Example", Permission: "ops.read", Script: "page.js"}}}}}
	raw, _ := json.Marshal(cfg)
	write("plugins.json", raw)
	return filepath.Join(dir, "plugins.json")
}
func TestRuntimePluginPageLoadsWithoutLiveProvider(t *testing.T) {
	f := pluginPageFixture(t)
	t.Setenv("ADMIN_PLUGINS_CONFIG", f)
	cfg := Config{}
	if err := loadPluginActions(&cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Extensions) != 1 || !cfg.Extensions[0].runtimePlugin {
		t.Fatal("runtime page not registered")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(f), "page.js"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg = Config{}
	if loadPluginActions(&cfg) == nil {
		t.Fatal("tampered plugin script accepted")
	}
}
func TestRuntimePluginAuthenticationAndOfflineHealthMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	t.Setenv("ADMIN_PLUGINS_CONFIG", pluginPageFixture(t))
	if err := loadPluginActions(&cfg); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	check(t, call(r, nil, "GET", "/api/v1/plugins/example/health", nil, nil), 401)
	check(t, call(r, nil, "GET", "/extensions/example/assets/page.js", nil, nil), 401)
	pw := "synthetic-plugin-page-password"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": pw}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", pw)
	check(t, call(r, owner, "GET", "/api/v1/extensions", nil, nil), 200)
	check(t, call(r, owner, "GET", "/extensions/example/assets/page.js", nil, nil), 200)
	check(t, call(r, owner, "GET", "/extensions/example/assets/missing.js", nil, nil), 404)
	check(t, call(r, owner, "GET", "/api/v1/plugins/example/health", nil, nil), 503)
	check(t, call(r, owner, "GET", "/api/v1/session", nil, nil), 200)
}

func TestRuntimePluginResourcesMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	file := pluginPageFixture(t)
	var installed PluginFile
	if err := plugin.ReadConfig(file, &installed); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "sgpr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	p := &installed.Providers[0]
	p.Socket = filepath.Join(socketDir, "rpc.sock")
	p.Routes = []PluginRouteConfig{{Method: "GET", Path: "/items", Permission: "ops.read"}, {Method: "POST", Path: "/items", Permission: "ops.write"}}
	raw, _ := json.Marshal(installed)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	endpoint := p.Endpoint
	handler := plugin.Handler(endpoint, func(_ context.Context, r plugin.Request) (any, error) {
		calls.Add(1)
		if r.Operation != "resource" || r.Resource == nil || r.Resource.Actor.ID == 0 || r.Resource.Actor.Role != "superadmin" || r.Resource.Path != "/items" {
			t.Error("unverified resource identity")
		}
		if r.Resource.Method == "POST" && r.Resource.OperationID != "stable-request" {
			t.Error("operation identity lost")
		}
		return plugin.ResourceResponse{Status: 200, Body: json.RawMessage(`{"ok":true}`)}, nil
	})
	listener, err := net.Listen("unix", endpoint.Socket)
	if err != nil {
		t.Fatal(err)
	}
	provider := &http.Server{Handler: handler}
	go provider.Serve(listener)
	defer provider.Close()
	t.Setenv("ADMIN_PLUGINS_CONFIG", file)
	if err := loadPluginActions(&cfg); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	router := s.Router()
	check(t, call(router, nil, "GET", "/api/v1/plugins/example/items", nil, nil), 401)
	password := "synthetic-resource-password"
	check(t, call(router, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, router, "owner", password)
	check(t, call(router, owner, "GET", "/api/v1/plugins/example/items", nil, nil), 200)
	check(t, call(router, owner, "POST", "/api/v1/plugins/example/items", map[string]string{"actor": "forged"}, nil), 400)
	check(t, call(router, owner, "POST", "/api/v1/plugins/example/items", map[string]string{"actor": "forged"}, map[string]string{"X-Operation-ID": "stable-request"}), 200)
	if calls.Load() != 2 {
		t.Fatal("rejected operation reached plugin")
	}
}

func TestRuntimePluginExplicitPermissionLoadsWithoutGrantingOtherRoles(t *testing.T) {
	file := pluginPageFixture(t)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var installation map[string]any
	if err := json.Unmarshal(raw, &installation); err != nil {
		t.Fatal(err)
	}
	provider := installation["providers"].([]any)[0].(map[string]any)
	provider["permissions"] = []string{"example.stats.read"}
	provider["pages"].([]any)[0].(map[string]any)["permission"] = "example.stats.read"
	provider["routes"] = []map[string]string{{"method": "GET", "path": "/statistics", "permission": "example.stats.read"}}
	raw, _ = json.Marshal(installation)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_PLUGINS_CONFIG", file)
	cfg := Config{}
	if err := loadPluginActions(&cfg); err != nil {
		t.Fatalf("explicit application permission must register: %v", err)
	}
	if err := validateExtensions(cfg.Extensions); err != nil {
		t.Fatal(err)
	}
	for _, policy := range cfg.Extensions[0].Policies {
		if policy.Permission == "example.stats.read" && policy.Role != "superadmin" {
			t.Fatal("custom permission granted to an unconfigured role")
		}
	}
	delete(provider, "permissions")
	raw, _ = json.Marshal(installation)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if loadPluginActions(&Config{}) == nil {
		t.Fatal("undeclared application permission accepted")
	}
}
