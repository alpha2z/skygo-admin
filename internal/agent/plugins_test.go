package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/plugin"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginTestConfig(t *testing.T) Config {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	raw, _ := json.Marshal(plugin.Manifest{Protocol: 1, CoreProtocol: 1, ID: "sample", Version: "1.0.0", Capabilities: []string{"sample-action"}})
	b, _ := json.Marshal(plugin.SignedManifest{Payload: raw, Signature: ed25519.Sign(key, raw)})
	_, rev, err := plugin.VerifyManifest(b, pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mf := filepath.Join(dir, "manifest.json")
	kf := filepath.Join(dir, "public.key")
	os.WriteFile(mf, b, 0600)
	os.WriteFile(kf, []byte(base64.StdEncoding.EncodeToString(pub)), 0600)

	return Config{Services: []LocalService{{ID: "one", Extensions: []string{"sample-action"}}, {ID: "two"}}, Plugins: []PluginConfig{{Endpoint: plugin.Endpoint{ID: "sample", Socket: "/missing/plugin.sock", Revision: rev}, Trust: plugin.Trust{ManifestFile: mf, PublicKeyFile: kf}, Actions: []string{"sample-action"}, Services: []string{"one"}}}}
}
func TestPluginUnavailableFailsClosed(t *testing.T) {
	c := pluginTestConfig(t)
	if err := installPlugins(&c); err != nil {
		t.Fatal(err)
	}
	if c.Guard(context.Background(), c.Services[0], control.Command{Action: "restart"}) == nil {
		t.Fatal("missing plugin allowed restart")
	}
	if err := c.Guard(context.Background(), c.Services[1], control.Command{Action: "health"}); err != nil {
		t.Fatal("unrelated service blocked")
	}
	x := c.Extensions["sample-action"]
	if r := x.Execute(context.Background(), c.Services[1], control.Command{ID: "job"}); r.Code != "PLUGIN_SERVICE_NOT_AUTHORIZED" {
		t.Fatal("unbound service accepted")
	}
	if r := x.Reconcile(context.Background(), c.Services[0], control.Command{ID: "job"}); r.Status != "uncertain" {
		t.Fatal("missing plugin lost uncertain state")
	}
}
func TestPluginRequiresExplicitServiceAndActionAuthorization(t *testing.T) {
	c := pluginTestConfig(t)
	c.Services[0].Extensions = nil
	if installPlugins(&c) == nil {
		t.Fatal("implicit action authorization")
	}
	c = pluginTestConfig(t)
	c.Plugins[0].Services = []string{"missing"}
	if installPlugins(&c) == nil {
		t.Fatal("unknown service accepted")
	}
	c = pluginTestConfig(t)
	c.Plugins = append(c.Plugins, c.Plugins[0])
	if installPlugins(&c) == nil {
		t.Fatal("duplicate plugin accepted")
	}
}

func TestPluginRevisionChangeDoesNotReplayOrReconcileWithNewPlugin(t *testing.T) {
	a, key, d := fixture(t)
	a.cfg.Services[0].PluginRevision = strings.Repeat("a", 64)
	c := command()
	c.PluginRevision = strings.Repeat("b", 64)
	signed, _ := control.Sign(c, key)
	if r := a.Handle(context.Background(), signed); r.Code != "PLUGIN_REVISION_CHANGED" || d.calls != 0 {
		t.Fatal("changed plugin executed command")
	}
	c.PluginRevision = a.cfg.Services[0].PluginRevision
	signed, _ = control.Sign(c, key)
	if r := a.Handle(context.Background(), signed); r.Status != "succeeded" {
		t.Fatal("matching revision rejected")
	}
	a.cfg.Services[0].PluginRevision = strings.Repeat("c", 64)
	if r := a.Handle(context.Background(), signed); r.Status != "succeeded" || d.calls != 1 {
		t.Fatal("terminal receipt replay depended on new plugin")
	}
	c.ID = "interrupted-plugin"
	signed, _ = control.Sign(c, key)
	if err := a.save(filepath.Join(a.cfg.StateDir, c.ID+".json"), receipt{Digest: control.Digest(signed.Payload), Command: c, Result: control.Result{ID: c.ID, Status: "uncertain"}}); err != nil {
		t.Fatal(err)
	}
	if r := a.Handle(context.Background(), signed); r.Status != "uncertain" || r.Code != "PLUGIN_REVISION_CHANGED" || d.calls != 1 {
		t.Fatal("interrupted operation migrated to changed plugin")
	}
}
