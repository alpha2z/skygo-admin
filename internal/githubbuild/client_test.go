package githubbuild

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchAllowlistAndCredentialIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	os.WriteFile(path, []byte(strings.Repeat("synthetic-", 5)), 0600)
	client, err := New(Config{Repository: "example/tooling", Workflow: "build.yml", AllowedRefs: []string{"main"}, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if client.Config().TokenFile != "" {
		t.Fatal("credential path exposed")
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "" {
			t.Error("missing provider credential")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	client.origin = server.URL
	if client.Dispatch(context.Background(), "unapproved", "admin-api", "linux/amd64") == nil || calls != 0 {
		t.Fatal("unapproved dispatch sent")
	}
	if client.Dispatch(context.Background(), "main", "admin-api", "linux/amd64") != nil || calls != 1 {
		t.Fatal("approved dispatch failed")
	}
	os.Chmod(path, 0644)
	if client.Dispatch(context.Background(), "main", "admin-api", "linux/amd64") == nil || calls != 1 {
		t.Fatal("public credential file accepted")
	}
}
