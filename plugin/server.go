package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Handler receives only requests matching the installed protocol and revision.
// The caller is authenticated by the operator-owned Unix socket permissions.
// A plugin must independently resolve service IDs against its local inventory.
func Handler(endpoint Endpoint, call func(context.Context, Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint.Validate() != nil || call == nil {
			http.Error(w, "plugin unavailable", 503)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/call" {
			http.NotFound(w, r)
			return
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody))
		d.DisallowUnknownFields()
		var req Request
		if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid plugin request", 400)
			return
		}
		if req.Version != Version || req.Plugin != endpoint.ID || req.Revision != endpoint.Revision {
			http.Error(w, "plugin contract mismatch", 409)
			return
		}
		switch req.Operation {
		case "health", "validate", "observe", "guard", "execute", "reconcile", "resource", "workflow-plan", "workflow-validate":
		default:
			http.Error(w, "unsupported plugin operation", 400)
			return
		}
		out, err := call(r.Context(), req)
		if err != nil {
			http.Error(w, "plugin rejected operation", 422)
			return
		}
		data, err := json.Marshal(out)
		if err != nil || len(data) > MaxBody/2 {
			http.Error(w, "invalid plugin result", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{Version: Version, Revision: endpoint.Revision, Data: data})
	})
}
