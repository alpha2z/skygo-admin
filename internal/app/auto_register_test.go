package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/githubbuild"
	"github.com/alpha2z/skygo-admin/internal/release"
	"strings"
	"testing"
	"time"
)

type syncBuilds struct {
	config    githubbuild.Config
	runs      []githubbuild.Run
	artifacts map[int64]githubbuild.SignedArtifact
	calls     int
	fail      bool
}

func (f *syncBuilds) Config() githubbuild.Config                             { return f.config }
func (f *syncBuilds) Runs(context.Context) ([]githubbuild.Run, error)        { return f.runs, nil }
func (f *syncBuilds) SyncRuns(context.Context) ([]githubbuild.Run, error)    { return f.runs, nil }
func (f *syncBuilds) ValidateDispatch(string, string, string) error          { return nil }
func (f *syncBuilds) Dispatch(context.Context, string, string, string) error { return nil }
func (f *syncBuilds) SignedImages(_ context.Context, id int64) (githubbuild.SignedArtifact, error) {
	f.calls++
	if f.fail {
		return githubbuild.SignedArtifact{}, errors.New("synthetic failure")
	}
	return f.artifacts[id], nil
}
func TestRegistrationBackoff(t *testing.T) {
	r := registrationRetry{}
	now := time.Now()
	for _, delay := range []time.Duration{30, 60, 120, 240, 480, 480, 480} {
		r = r.failed(now)
		if r.Delay != delay*time.Second || !r.Next.Equal(now.Add(delay*time.Second)) {
			t.Fatal("backoff bounds")
		}
	}
}
func TestAutomaticRegistrationMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	db.Create(&BuildTrustKey{ID: control.Digest(pub), PublicKey: base64.StdEncoding.EncodeToString(pub)})
	f := &syncBuilds{config: githubbuild.Config{Repository: "example/tooling", Workflow: "build.yml", AllowedRefs: []string{"main"}}, artifacts: map[int64]githubbuild.SignedArtifact{}}
	success := "success"
	for i := int64(1); i <= 5; i++ {
		run := githubbuild.Run{ID: i, Attempt: 1, Ref: "main", Commit: strings.Repeat("a", 40), Status: "completed", Conclusion: &success, Event: "push"}
		m := release.Manifest{Version: 1, ID: registrationID(run), Build: release.Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: run.Commit, RunID: i, RunAttempt: 1}, Images: []release.Image{{Service: "admin-api", Platform: "linux/arm64", Reference: "example/api@sha256:" + strings.Repeat("a", 64)}}}
		signed, _ := release.Sign(m, key)
		f.runs = append(f.runs, run)
		f.artifacts[i] = githubbuild.SignedArtifact{Run: run, Attempt: 1, Release: signed}
	}
	s.github = f
	retries := map[string]registrationRetry{}
	now := time.Now().UTC()
	s.autoRegisterCycle(context.Background(), retries, now)
	if f.calls != 0 {
		t.Fatal("default auto registration enabled")
	}
	f.config.AutoRegister = true
	f.fail = true
	s.autoRegisterCycle(context.Background(), retries, now)
	if f.calls != 3 {
		t.Fatal("verification bound")
	}
	s.autoRegisterCycle(context.Background(), retries, now)
	if f.calls != 5 {
		t.Fatal("backoff bypassed")
	}
	f.fail = false
	s.autoRegisterCycle(context.Background(), retries, now.Add(30*time.Second))
	if f.calls != 8 {
		t.Fatal("retry budget")
	}
	s.autoRegisterCycle(context.Background(), retries, now.Add(time.Minute))
	if f.calls != 10 {
		t.Fatal("registered items refetched")
	}
	s.autoRegisterCycle(context.Background(), retries, now.Add(2*time.Minute))
	if f.calls != 10 {
		t.Fatal("idempotence")
	}
	var audits []Audit
	db.Where("action = ?", "system.build.register").Find(&audits)
	if len(audits) != 5 {
		t.Fatal("system audit missing")
	}
	for _, a := range audits {
		if a.OperatorID != 0 {
			t.Fatal("system impersonated admin")
		}
	}
	// Database lookup failure remains unknown and must not fetch/register again.
	if db.Migrator().RenameTable(&ImageRelease{}, "hidden_image_releases") != nil {
		t.Fatal("read failure fixture")
	}
	s.autoRegisterCycle(context.Background(), retries, now.Add(3*time.Minute))
	if f.calls != 10 {
		t.Fatal("unknown treated as unregistered")
	}
	db.Migrator().RenameTable("hidden_image_releases", &ImageRelease{})
}
