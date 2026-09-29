// Two instances of this synthetic service demonstrate optional Skygo Cluster.
package main

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"github.com/scott4game/skygo/actor"
	skyapp "github.com/scott4game/skygo/app"
	"github.com/scott4game/skygo/cluster"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	id := os.Getenv("NODE_ID")
	listen := os.Getenv("NODE_LISTEN")
	registry := os.Getenv("NODE_REGISTRY")
	httpListen := os.Getenv("NODE_HTTP")
	secret, err := settings.Secret(os.Getenv("NODE_SECRET_FILE"))
	if err != nil {
		log.Fatal("node secret unavailable")
	}
	system := actor.NewSystem(actor.SystemOptions{})
	service, _, err := system.Reserve("probe", actor.ServiceOptions{})
	if err != nil {
		log.Fatal("example service initialization failed")
	}
	if err = service.Start(context.Background()); err != nil {
		log.Fatal("example service startup failed")
	}
	node, err := cluster.New(cluster.Config{NodeID: id, Listen: listen, Secret: []byte(secret), Registry: cluster.FileRegistry{Path: registry}}, system)
	if err != nil {
		log.Fatal("invalid node configuration")
	}
	r := &skyapp.Runtime{}
	r.Add("cluster", node)
	var server *http.Server
	r.Add("health", skyapp.ComponentFuncs{StartFunc: func(ctx context.Context) error {
		mux := http.NewServeMux()
		mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			snapshot, err := (cluster.FileRegistry{Path: registry}).Load(ctx)
			if err != nil {
				w.WriteHeader(503)
				return
			}
			peers := map[string]bool{}
			for _, endpoint := range snapshot.Nodes {
				if endpoint.NodeID == id {
					continue
				}
				_, err := node.Resolve(ctx, endpoint.NodeID, "probe")
				peers[endpoint.NodeID] = err == nil
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(peers)
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			snapshot, err := (cluster.FileRegistry{Path: registry}).Load(r.Context())
			if err != nil {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "node": id, "revision": snapshot.Revision})
		})
		l, err := net.Listen("tcp", httpListen)
		if err != nil {
			return err
		}
		server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go server.Serve(l)
		return nil
	}, StopFunc: func(ctx context.Context) error {
		if server != nil {
			return server.Shutdown(ctx)
		}
		return nil
	}})
	if r.Run(context.Background(), 15*time.Second) != nil {
		log.Fatal("example runtime failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	system.Stop(ctx)
}
