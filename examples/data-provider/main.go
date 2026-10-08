// data-provider serves operator-configured synthetic datasets through the plugin API.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/alpha2z/skygo-admin/plugin"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Endpoint plugin.Endpoint    `json:"endpoint"`
	Catalog  plugin.DataCatalog `json:"catalog"`
	Values   map[string]float64 `json:"values"`
}

func main() {
	file := flag.String("config", "", "restricted example configuration")
	initDir := flag.String("init", "", "create a synthetic signed installation in a new directory")
	flag.Parse()
	if *initDir != "" {
		if err := initialize(*initDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	var cfg config
	if plugin.ReadConfig(*file, &cfg) != nil || cfg.Endpoint.Validate() != nil || cfg.Catalog.Validate() != nil {
		fmt.Fprintln(os.Stderr, "invalid example configuration")
		os.Exit(1)
	}
	handler := plugin.Handler(cfg.Endpoint, func(ctx context.Context, r plugin.Request) (any, error) {
		if r.Operation == "health" {
			return map[string]bool{"ready": true}, nil
		}
		respond := func(status int, v any) (any, error) {
			b, e := json.Marshal(v)
			return plugin.ResourceResponse{Status: status, Body: b}, e
		}
		if r.Operation != "resource" || r.Resource == nil || r.Resource.Actor.ID == 0 || r.Resource.Actor.Permission != "ops.read" || r.Resource.Method != "GET" {
			return respond(403, map[string]string{"error": "read permission required"})
		}
		if r.Resource.Path == "/data/catalog" {
			return respond(200, cfg.Catalog)
		}
		parts := strings.Split(strings.TrimPrefix(r.Resource.Path, "/data/datasets/"), "/")
		if len(parts) != 2 {
			return respond(404, map[string]string{"error": "unknown dataset"})
		}
		d, ok := cfg.Catalog.Dataset(parts[0])
		if !ok {
			return respond(404, map[string]string{"error": "unknown dataset"})
		}
		if e := plugin.ValidateDataQuery(d, parts[1], r.Resource.Query); e != nil {
			return respond(400, map[string]string{"error": e.Error()})
		}
		now := time.Now().UTC()
		v := cfg.Values[d.ID]
		result := plugin.DataResult{Version: 1, SampledAt: &now, State: "ok", Warnings: []string{}}
		switch parts[1] {
		case "current":
			result.Values = []plugin.DataValue{{Value: &v}}
		case "series":
			start, _ := time.Parse(time.RFC3339, r.Resource.Query["start"][0])
			end, _ := time.Parse(time.RFC3339, r.Resource.Query["end"][0])
			step, _ := time.ParseDuration(r.Resource.Query["step"][0] + "s")
			s := plugin.DataSeries{}
			for t := start; !t.After(end); t = t.Add(step) {
				s.Points = append(s.Points, plugin.DataPoint{At: t, Value: &v})
			}
			result.Series = []plugin.DataSeries{s}
		case "rows":
			result.Rows = []map[string]any{}
		}
		return respond(200, result)
	})
	listener, err := net.Listen("unix", cfg.Endpoint.Socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "socket unavailable")
		os.Exit(1)
	}
	defer listener.Close()
	if os.Chmod(cfg.Endpoint.Socket, 0600) != nil {
		os.Exit(1)
	}
	server := http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	if server.Serve(listener) != http.ErrServerClosed {
		os.Exit(1)
	}
}

// initialize creates disposable demo trust material, never production credentials.
func initialize(directory string) error {
	dir, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	manifest := plugin.Manifest{Protocol: 1, CoreProtocol: 1, ID: "sample", Version: "demo-1", Capabilities: []string{"data-read"}, Assets: map[string]string{}}
	payload, _ := json.Marshal(manifest)
	signed, _ := json.Marshal(plugin.SignedManifest{Payload: payload, Signature: ed25519.Sign(key, payload)})
	_, revision, err := plugin.VerifyManifest(signed, public)
	if err != nil {
		return err
	}
	endpoint := plugin.Endpoint{ID: "sample", Socket: filepath.Join(dir, "provider.sock"), Revision: revision}
	cfg := config{Endpoint: endpoint, Catalog: plugin.DataCatalog{Version: 1, Datasets: []plugin.DataSet{{ID: "queue-depth", Title: "Synthetic queue depth", Unit: "items", Modes: []string{"current", "series"}, Dimensions: []string{}}}, Panels: []plugin.DataPanel{{ID: "overview", Title: "Synthetic monitoring", Widgets: []plugin.DataWidget{{Dataset: "queue-depth", Kind: "card", Title: "Queue depth"}, {Dataset: "queue-depth", Kind: "chart", Title: "Synthetic history"}}}}}, Values: map[string]float64{"queue-depth": 12}}
	provider := map[string]any{"id": endpoint.ID, "socket": endpoint.Socket, "revision": endpoint.Revision, "manifest_file": filepath.Join(dir, "manifest.json"), "public_key_file": filepath.Join(dir, "trust.pub"), "actions": []any{}, "data_panels": []any{map[string]string{"id": "overview", "title": "Synthetic monitoring", "permission": "ops.read"}}}
	configRaw, _ := json.MarshalIndent(cfg, "", "  ")
	installRaw, _ := json.MarshalIndent(map[string]any{"version": 1, "providers": []any{provider}}, "", "  ")
	for name, b := range map[string][]byte{"manifest.json": signed, "trust.pub": []byte(base64.StdEncoding.EncodeToString(public)), "provider.json": configRaw, "admin-plugins.json": installRaw} {
		if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			return err
		}
	}
	return nil
}
