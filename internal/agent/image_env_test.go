package agent

import (
	"context"
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageEnvPreservesContentAndRestoresAbsentVariables(t *testing.T) {
	for _, original := range []string{"# local settings\nAPI_IMAGE=example/api:old\nOTHER=unchanged\n", "# local settings\r\nAPI_IMAGE=example/api:old\r\nOTHER=unchanged\r\n", "OTHER=unchanged", ""} {
		t.Run(string(rune(len(original)+65)), func(t *testing.T) {
			a, _, _ := fixture(t)
			path := filepath.Join(t.TempDir(), "managed.env")
			os.WriteFile(path, []byte(original), 0600)
			line, present, err := envLine([]byte(original), "API_IMAGE")
			if err != nil {
				t.Fatal(err)
			}
			target := "API_IMAGE=example/api@sha256:" + strings.Repeat("a", 64)
			if strings.Contains(original, "\r\n") {
				target += "\r"
			}
			j := envJournal{Files: []envFileEdit{{Path: path, Before: a.envHash([]byte(original)), TrailingNewline: strings.HasSuffix(original, "\n"), Edits: []envEdit{{Key: "API_IMAGE", Original: line, Present: present, Target: target}}}}}
			b, _ := json.Marshal(j)
			settings.Atomic(a.envPath("test"), b)
			if err := a.applyEnv("test", false); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			if !strings.Contains(string(raw), target) {
				t.Fatal("image not persisted")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal("permissions changed")
			}
			if err := a.applyEnv("test", true); err != nil {
				t.Fatal(err)
			}
			raw, _ = os.ReadFile(path)
			if string(raw) != original {
				t.Fatal("original content not restored")
			}
		})
	}
}
func TestEnvExternalEditNeverOverwritten(t *testing.T) {
	a, _, _ := fixture(t)
	path := filepath.Join(t.TempDir(), "managed.env")
	original := "API_IMAGE=example/api:old\nOTHER=original\n"
	os.WriteFile(path, []byte(original), 0600)
	j := envJournal{Files: []envFileEdit{{Path: path, Before: a.envHash([]byte(original)), TrailingNewline: true, Edits: []envEdit{{Key: "API_IMAGE", Original: "API_IMAGE=example/api:old", Present: true, Target: "API_IMAGE=example/api:new"}}}}}
	b, _ := json.Marshal(j)
	settings.Atomic(a.envPath("test"), b)
	if a.applyEnv("test", false) != nil {
		t.Fatal("write")
	}
	external := "API_IMAGE=example/api:new\nOTHER=external\n"
	os.WriteFile(path, []byte(external), 0600)
	if a.applyEnv("test", true) == nil {
		t.Fatal("external edit overwritten")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != external {
		t.Fatal("external content changed")
	}
	if _, _, err := envLine([]byte("API_IMAGE=a\nexport API_IMAGE=b\n"), "API_IMAGE"); err == nil {
		t.Fatal("duplicate variables accepted")
	}
}
func TestDisabledEnvPlanningDoesNotReadOrWriteFiles(t *testing.T) {
	a, _, _ := fixture(t)
	if a.planEnv(context.Background(), "disabled", a.cfg.Services, []string{"unused"}) != nil {
		t.Fatal("disabled service required env")
	}
	if a.applyEnv("disabled", false) != nil {
		t.Fatal("empty env plan")
	}
}
