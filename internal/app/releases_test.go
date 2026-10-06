package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/githubbuild"
	"github.com/alpha2z/skygo-admin/internal/release"
	mysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBuilds struct {
	run    githubbuild.Run
	signed release.SignedManifest
}

func (f *fakeBuilds) Config() githubbuild.Config {
	return githubbuild.Config{Repository: "example/tooling", Workflow: "build.yml", AllowedRefs: []string{"main"}}
}
func (f *fakeBuilds) Runs(context.Context) ([]githubbuild.Run, error) {
	return []githubbuild.Run{f.run}, nil
}
func (f *fakeBuilds) ValidateDispatch(string, string, string) error          { return nil }
func (f *fakeBuilds) Dispatch(context.Context, string, string, string) error { return nil }
func (f *fakeBuilds) SignedImages(context.Context, int64) (githubbuild.SignedArtifact, error) {
	return githubbuild.SignedArtifact{Run: f.run, Attempt: f.run.Attempt, Release: f.signed}, nil
}
func releaseDB(t *testing.T) (*gorm.DB, Config) {
	t.Helper()
	dsn := os.Getenv("SKYGO_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL required")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "skygo_admin_test_") {
		t.Fatal("isolated database required")
	}
	base, err := OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := cfg.DBName + "_releases"
	if base.Exec("CREATE DATABASE `"+name+"`").Error != nil {
		t.Fatal("database fixture failed")
	}
	cfg.DBName = name
	db, err := OpenDB(cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sql, _ := db.DB()
		sql.Close()
		base.Exec("DROP DATABASE `" + name + "`")
		sql, _ = base.DB()
		sql.Close()
	})
	if Migrate(db) != nil {
		t.Fatal("migration failed")
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	return db, Config{IndependentApprovalEnabled: true, JWTSecret: strings.Repeat("j", 40), BootstrapToken: strings.Repeat("b", 40), SigningKey: key, SkipEmailConfirmation: true, WebRoot: "../../admin-web"}
}
func TestReleaseRegistrationAndPreparationMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	// Recreate the actual v1 shape, not merely its version marker.
	if db.Migrator().DropTable(&BuildTrustKey{}, &ImageRelease{}, &ImagePreparation{}) != nil || db.Migrator().DropColumn(&Task{}, "FinishedAt") != nil {
		t.Fatal("v1 schema fixture failed")
	}
	db.Save(&SchemaVersion{ID: 1, Version: 1})
	if db.Create(&Session{ID: "legacy-session", AdminID: 0, ExpiresAt: time.Now().Add(time.Hour)}).Error != nil {
		t.Fatal("v1 data fixture failed")
	}
	if _, err := NewServer(cfg, db); err == nil {
		t.Fatal("migration requirement bypassed")
	}
	if Migrate(db) != nil || !db.Migrator().HasColumn(&Task{}, "FinishedAt") {
		t.Fatal("v1 to v2 migration failed")
	}
	var legacy int64
	db.Model(&Session{}).Where("id = ?", "legacy-session").Count(&legacy)
	if legacy != 1 {
		t.Fatal("v1 session data lost")
	}
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	success := "success"
	fake := &fakeBuilds{run: githubbuild.Run{ID: 12, Attempt: 2, Ref: "main", Commit: strings.Repeat("a", 40), Status: "completed", Conclusion: &success, Event: "workflow_dispatch"}}
	s.github = fake
	router := s.Router()
	password := "synthetic-long-passphrase"
	check(t, call(router, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, router, "owner", password)
	check(t, call(router, owner, "POST", "/api/v1/admins", map[string]string{"Username": "observer", "Email": "observer@example.com", "Password": password, "Role": "viewer"}, nil), 200)
	viewer := signIn(t, s, router, "observer", password)
	check(t, call(router, viewer, "POST", "/api/v1/builds/12/register", map[string]int{"attempt": 2}, nil), 403)
	check(t, call(router, viewer, "POST", "/api/v1/release-settings/keys", map[string]string{"public_key": "invalid"}, nil), 403)
	check(t, call(router, viewer, "POST", "/api/v1/releases/ci-12-2/prepare-admin", map[string]string{"host_id": "control-host", "batch": "invalid"}, nil), 403)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := release.Manifest{Version: 1, ID: "ci-12-2", Build: release.Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: fake.run.Commit, RunID: 12, RunAttempt: 2}, Images: []release.Image{{Service: "admin-api", Platform: "linux/arm64", Reference: "example/api@sha256:" + strings.Repeat("a", 64)}, {Service: "admin-web", Platform: "linux/arm64", Reference: "example/web@sha256:" + strings.Repeat("b", 64)}}}
	fake.signed, _ = release.Sign(m, key)
	register := func() int {
		return call(router, owner, "POST", "/api/v1/builds/12/register", map[string]int{"attempt": 2}, nil).Code
	}
	if register() != 409 {
		t.Fatal("registration without trust accepted")
	}
	check(t, call(router, owner, "POST", "/api/v1/release-settings/keys", map[string]string{"public_key": base64.StdEncoding.EncodeToString(cfg.SigningKey.Public().(ed25519.PublicKey))}, nil), 400)
	check(t, call(router, owner, "POST", "/api/v1/release-settings/keys", map[string]string{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil), 200)
	if register() != 200 || register() != 200 {
		t.Fatal("registration failed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if register() != 200 {
				t.Error("concurrent duplicate registration failed")
			}
		}()
	}
	wg.Wait()
	var count int64
	db.Model(&Audit{}).Where("action = ?", "build.register").Count(&count)
	if count != 1 {
		t.Fatal("duplicate registration audit")
	}
	check(t, call(router, owner, "GET", "/api/v1/releases/ci-12-2", nil, nil), 200)
	status := s.registrations([]githubbuild.Run{fake.run}, fake.Config())[m.ID]
	if status.Status != "registered" {
		t.Fatal("persistent registration missing")
	}
	changedRun := fake.run
	changedRun.Attempt = 3
	if s.registrations([]githubbuild.Run{changedRun}, fake.Config())[registrationID(changedRun)].Status != "unregistered" {
		t.Fatal("new attempt inherited registration")
	}
	modified := m
	modified.Images = append([]release.Image(nil), m.Images...)
	modified.Images[0].Reference = "example/api@sha256:" + strings.Repeat("c", 64)
	fake.signed, _ = release.Sign(modified, key)
	if register() != 409 {
		t.Fatal("immutable registration overwritten")
	}
	fake.signed, _ = release.Sign(m, key)
	if db.Migrator().RenameTable(&ImageRelease{}, "hidden_image_releases") != nil {
		t.Fatal("read failure fixture")
	}
	unknown := s.registrations([]githubbuild.Run{fake.run}, fake.Config())[m.ID]
	db.Migrator().RenameTable("hidden_image_releases", &ImageRelease{})
	if unknown.Status != "unknown" {
		t.Fatal("read failure reported absent")
	}
	hostResponse := call(router, owner, "POST", "/api/v1/hosts", map[string]string{"id": "control-host"}, nil)
	check(t, hostResponse, 200)
	var enrolled struct{ Token string }
	json.Unmarshal(hostResponse.Body.Bytes(), &enrolled)
	headers := map[string]string{"X-Host-ID": "control-host", "Authorization": "Bearer " + enrolled.Token}
	observations := []control.Observation{}
	for _, image := range m.Images {
		id := "control-" + image.Service
		check(t, call(router, owner, "POST", "/api/v1/services", control.Service{ID: id, HostID: "control-host", Image: image.Reference, ControlPlane: true, ControlKind: image.Service, Platform: image.Platform}, nil), 200)
		observations = append(observations, control.Observation{Service: id, Image: image.Reference, ImageID: "sha256:" + strings.Repeat("f", 64), Platform: "linux/arm64", Capabilities: []string{"image.prepare.v1"}, Healthy: true})
	}
	heartbeat := func(boot string) {
		check(t, call(router, nil, "POST", "/agent/v1/heartbeat", control.Heartbeat{Version: 1, BootID: boot, Observations: observations}, headers), 200)
	}
	heartbeat("boot-a")
	groups := func() prepareGroup {
		t.Helper()
		response := call(router, owner, "GET", "/api/v1/releases/ci-12-2/admin-preparation", nil, nil)
		check(t, response, 200)
		var g []prepareGroup
		if json.Unmarshal(response.Body.Bytes(), &g) != nil || len(g) != 1 {
			t.Fatal("missing control group")
		}
		return g[0]
	}
	group := groups()
	if group.Status != "unprepared" || !group.CanPrepare {
		t.Fatal("valid control pair blocked")
	}
	payload := map[string]string{"host_id": "control-host", "batch": group.Batch}
	check(t, call(router, owner, "POST", "/api/v1/releases/ci-12-2/prepare-admin", map[string]string{"host_id": "control-host", "batch": "stale-preview"}, nil), 409)
	prepare := func() int {
		return call(router, owner, "POST", "/api/v1/releases/ci-12-2/prepare-admin", payload, nil).Code
	}
	if prepare() != 200 || prepare() != 200 {
		t.Fatal("preparation failed")
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if prepare() != 200 {
				t.Error("concurrent preparation failed")
			}
		}()
	}
	wg.Wait()
	db.Model(&Task{}).Where("action = ?", "prepare-image").Count(&count)
	if count != 2 {
		t.Fatal("duplicate preparation tasks")
	}
	db.Model(&Audit{}).Where("action = ?", "images.prepare").Count(&count)
	if count != 1 {
		t.Fatal("duplicate preparation audit")
	}
	commands := func() []control.Command {
		t.Helper()
		w := call(router, nil, "GET", "/agent/v1/commands", nil, headers)
		check(t, w, 200)
		var envelopes []control.Envelope
		json.Unmarshal(w.Body.Bytes(), &envelopes)
		out := []control.Command{}
		for _, e := range envelopes {
			cmd, err := control.Verify(e, cfg.SigningKey.Public().(ed25519.PublicKey), "control-host")
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, cmd)
		}
		return out
	}
	jobs := commands()
	if len(jobs) != 2 {
		t.Fatal("paired commands missing")
	}
	successResult := func(cmd control.Command) {
		check(t, call(router, nil, "POST", "/agent/v1/results", control.Result{ID: cmd.ID, Status: "succeeded", Code: "IMAGE_PREPARED", Image: cmd.Image, Platform: cmd.Platform, ImageID: "sha256:" + strings.Repeat("e", 64)}, headers), 200)
	}
	successResult(jobs[0])
	check(t, call(router, nil, "POST", "/agent/v1/results", control.Result{ID: jobs[1].ID, Status: "failed", Code: "IMAGE_PULL_FAILED"}, headers), 200)
	if groups().Status != "failed" {
		t.Fatal("partial failure hidden")
	}
	if prepare() != 200 {
		t.Fatal("partial retry failed")
	}
	retry := commands()
	if len(retry) != 1 || retry[0].ID == jobs[1].ID {
		t.Fatal("successful item repeated or failed command replayed")
	}
	successResult(retry[0])
	if groups().Status != "ready" {
		t.Fatal("verified pair not ready")
	}
	var records []ServiceRecord
	db.Find(&records)
	for _, r := range records {
		if r.BusyTask != "" {
			t.Fatal("preparation changed service ownership")
		}
	}
	heartbeat("boot-b")
	if groups().Status != "unknown" {
		t.Fatal("old boot receipt trusted")
	}
	heartbeat("boot-a")
	expired := time.Now().Add(-6 * time.Minute)
	db.Model(&Task{}).Where("action = ? AND status = ?", "prepare-image", "succeeded").Update("finished_at", expired)
	if groups().Status != "unknown" {
		t.Fatal("old receipt trusted")
	}
	db.Model(&Host{}).Where("id = ?", "control-host").Update("active", false)
	if groups().Status != "blocked" {
		t.Fatal("offline host ready")
	}
	db.Model(&Host{}).Where("id = ?", "control-host").Update("active", true)
	observations[1].Platform = "linux/amd64"
	heartbeat("boot-a")
	if groups().ErrorCode != "ADMIN_PLATFORM_MISMATCH" {
		t.Fatal("mixed architecture accepted")
	}
	observations[1].Platform = "linux/arm64"
	heartbeat("boot-a")
	partial := m
	partial.ID = "ci-13-2"
	partial.Build.RunID = 13
	partial.Images = partial.Images[:1]
	single, _ := release.Sign(partial, key)
	raw, _ := json.Marshal(single)
	db.Create(&ImageRelease{ID: partial.ID, Payload: string(raw), CreatedAt: time.Now().UTC()})
	response := call(router, owner, "GET", "/api/v1/releases/ci-13-2/admin-preparation", nil, nil)
	check(t, response, 200)
	var incomplete []prepareGroup
	json.Unmarshal(response.Body.Bytes(), &incomplete)
	if len(incomplete) != 1 || incomplete[0].ErrorCode != "ADMIN_PAIR_MISSING" {
		t.Fatal("incomplete release pair accepted")
	}
	check(t, call(router, owner, "POST", "/api/v1/services", control.Service{ID: "duplicate-api", HostID: "control-host", Image: m.Images[0].Reference, ControlPlane: true, ControlKind: "admin-api", Platform: "linux/arm64"}, nil), 200)
	if groups().ErrorCode != "ADMIN_TARGET_AMBIGUOUS" {
		t.Fatal("ambiguous host selection accepted")
	}
	// Explicit migration retains prior data and rejects old binaries until upgraded.
	db.Save(&SchemaVersion{ID: 1, Version: 1})
	if _, err = NewServer(cfg, db); err == nil {
		t.Fatal("old schema silently accepted")
	}
	if Migrate(db) != nil {
		t.Fatal("schema upgrade failed")
	}
	db.Model(&ImageRelease{}).Count(&count)
	if count != 2 {
		t.Fatal(fmt.Sprint("migration lost release data: ", count))
	}
}
