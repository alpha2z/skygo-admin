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

func TestSyncRunsBoundedPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	os.WriteFile(path, []byte(strings.Repeat("synthetic-", 5)), 0600)
	client, err := New(Config{Repository: "example/tooling", Workflow: "build.yml", AllowedRefs: []string{"main"}, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("per_page") != "30" {
			t.Error("unbounded page")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"workflow_runs":[`))
		for i := 0; i < 30; i++ {
			if i > 0 {
				w.Write([]byte(","))
			}
			w.Write([]byte(`{"id":1,"head_branch":"main","run_attempt":1}`))
		}
		w.Write([]byte(`]}`))
	}))
	defer server.Close()
	client.origin = server.URL
	runs, err := client.SyncRuns(context.Background())
	if err != nil || len(runs) != 150 || calls != 5 {
		t.Fatal("sync pagination bounds")
	}
	calls = 0
	runs, err = client.Runs(context.Background())
	if err != nil || len(runs) != 30 || calls != 1 {
		t.Fatal("interactive query changed")
	}
}
