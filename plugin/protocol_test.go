package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalPluginProtocol(t *testing.T) {
	dir, err := os.MkdirTemp("", "sgp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := Endpoint{ID: "sample", Socket: filepath.Join(dir, "p.sock"), Revision: strings.Repeat("a", 64)}
	l, err := net.Listen("unix", e.Socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := &http.Server{Handler: Handler(e, func(_ context.Context, r Request) (any, error) {
		calls.Add(1)
		if r.Operation == "execute" {
			return nil, errors.New("private detail must not escape")
		}
		return map[string]bool{"healthy": true}, nil
	})}
	go s.Serve(l)
	defer s.Close()
	var got map[string]bool
	if err := e.Call(context.Background(), Request{Operation: "health"}, &got); err != nil || !got["healthy"] {
		t.Fatalf("health: %v", err)
	}
	wrong := e
	wrong.Revision = strings.Repeat("b", 64)
	if err := wrong.Call(context.Background(), Request{Operation: "health"}, nil); err == nil {
		t.Fatal("revision mismatch accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("mismatched request reached plugin")
	}
	if err := e.Call(context.Background(), Request{Operation: "execute"}, nil); err == nil || strings.Contains(err.Error(), "private detail") {
		t.Fatal("unsafe error response")
	}
	if calls.Load() != 2 {
		t.Fatal("execution retried")
	}
	if err := e.Call(context.Background(), Request{Operation: "shell"}, nil); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if err := e.Call(context.Background(), Request{Operation: "validate", Payload: json.RawMessage(`"` + strings.Repeat("x", MaxBody) + `"`)}, nil); err == nil {
		t.Fatal("oversized payload accepted")
	}
}
func TestReadConfigRejectsUnsafeFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plugin.json")
	if err := os.WriteFile(p, []byte(`{"id":"sample"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := ReadConfig(p, &v); err != nil {
		t.Fatal(err)
	}
	link := p + ".link"
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if ReadConfig(link, &v) == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(p, 0666); err != nil {
		t.Fatal(err)
	}
	if ReadConfig(p, &v) == nil {
		t.Fatal("writable configuration accepted")
	}
}
