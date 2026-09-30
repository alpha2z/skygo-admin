package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type envEdit struct {
	Key, Original, Target string
	Present               bool
}
type envFileEdit struct {
	Path, Before    string
	TrailingNewline bool
	Edits           []envEdit
}
type envJournal struct{ Files []envFileEdit }

func envLine(raw []byte, key string) (string, bool, error) {
	found := false
	line := ""
	for _, s := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "export "), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			if found {
				return "", false, errors.New("duplicate image variable")
			}
			found = true
			line = s
		}
	}
	return line, found, nil
}
func replaceLine(raw []byte, key, line string, present bool) []byte {
	lines := strings.Split(string(raw), "\n")
	for i, s := range lines {
		parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "export "), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			if present {
				lines[i] = line
			} else {
				lines = append(lines[:i], lines[i+1:]...)
			}
			return []byte(strings.Join(lines, "\n"))
		}
	}
	if !present {
		return raw
	}
	sep := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		sep = "\r\n"
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		raw = append(raw, sep...)
	}
	return append(raw, []byte(line+"\n")...)
}
func (a *Agent) envHash(raw []byte) string {
	h := hmac.New(sha256.New, []byte(a.token))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func readEnv(path string) ([]byte, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, errors.New("environment unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, nil, errors.New("invalid environment file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 2<<20+1))
	return raw, info, err
}
func writeEnv(path string, raw []byte, info os.FileInfo) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".admin-image-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		current, _ := f.Stat()
		owner := current.Sys().(*syscall.Stat_t)
		if owner.Uid != stat.Uid || owner.Gid != stat.Gid {
			if f.Chown(int(stat.Uid), int(stat.Gid)) != nil {
				return errors.New("environment owner changed")
			}
		}
	}
	if f.Chmod(info.Mode().Perm()) != nil {
		return errors.New("environment permissions unavailable")
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("environment write failed")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (a *Agent) envPath(id string) string {
	return filepath.Join(a.cfg.StateDir, id+".env-journal.json")
}
func (a *Agent) readEnvJournal(id string) (envJournal, error) {
	var j envJournal
	b, err := os.ReadFile(a.envPath(id))
	if err != nil {
		return j, err
	}
	if json.Unmarshal(b, &j) != nil {
		return j, errors.New("invalid environment journal")
	}
	return j, nil
}
func (a *Agent) planEnv(ctx context.Context, id string, services []LocalService, images []string) error {
	if _, err := os.Stat(a.envPath(id)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	j := envJournal{}
	for i, s := range services {
		if !s.SyncImageEnv {
			continue
		}
		if s.EnvFile == "" || !control.Image.MatchString(images[i]) {
			return errors.New("image environment not configured")
		}
		// The template itself must use the inventory's dedicated image variable.
		b, err := run(ctx, os.Environ(), append(compose(s), "config", "--no-interpolate", "--format", "json")...)
		if err != nil {
			return err
		}
		var template struct {
			Services map[string]struct{ Image string }
		}
		if json.Unmarshal(b, &template) != nil {
			return errors.New("invalid compose template")
		}
		expression := template.Services[s.ComposeService].Image
		if expression != "${"+s.ImageVariable+"}" && !strings.HasPrefix(expression, "${"+s.ImageVariable+":-") && !strings.HasPrefix(expression, "${"+s.ImageVariable+":?") {
			return errors.New("image variable not owned by service")
		}
		before, err := run(ctx, os.Environ(), append(compose(s), "config", "--format", "json")...)
		if err != nil {
			return err
		}
		after, err := run(ctx, imageEnv(s, images[i]), append(compose(s), "config", "--format", "json")...)
		if err != nil || !onlyServiceImageChanged(before, after, s.ComposeService) {
			return errors.New("image variable changes unrelated configuration")
		}
		raw, _, err := readEnv(s.EnvFile)
		if err != nil {
			return err
		}
		line, present, err := envLine(raw, s.ImageVariable)
		if err != nil {
			return err
		}
		index := -1
		for n, f := range j.Files {
			if f.Path == s.EnvFile {
				index = n
			}
		}
		if index < 0 {
			j.Files = append(j.Files, envFileEdit{Path: s.EnvFile, Before: a.envHash(raw), TrailingNewline: bytes.HasSuffix(raw, []byte("\n"))})
			index = len(j.Files) - 1
		}
		for _, edit := range j.Files[index].Edits {
			if edit.Key == s.ImageVariable {
				return errors.New("shared image variable")
			}
		}
		target := s.ImageVariable + "=" + images[i]
		if bytes.Contains(raw, []byte("\r\n")) {
			target += "\r"
		}
		j.Files[index].Edits = append(j.Files[index].Edits, envEdit{Key: s.ImageVariable, Original: line, Present: present, Target: target})
	}
	b, _ := json.Marshal(j)
	return settings.Atomic(a.envPath(id), b)
}
func (a *Agent) normalizedEnv(f envFileEdit) ([]byte, error) {
	raw, _, err := readEnv(f.Path)
	if err != nil {
		return nil, err
	}
	for _, e := range f.Edits {
		line, present, err := envLine(raw, e.Key)
		if err != nil {
			return nil, err
		}
		if !(present && line == e.Target) && !(present == e.Present && line == e.Original) {
			return nil, errors.New("image environment externally modified")
		}
		raw = replaceLine(raw, e.Key, e.Original, e.Present)
	}
	if !f.TrailingNewline {
		raw = bytes.TrimSuffix(raw, []byte("\n"))
		raw = bytes.TrimSuffix(raw, []byte("\r"))
	}
	if a.envHash(raw) != f.Before {
		return nil, errors.New("environment configuration drift")
	}
	return raw, nil
}
func (a *Agent) applyEnv(id string, restore bool) error {
	j, err := a.readEnvJournal(id)
	if os.IsNotExist(err) && restore {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range j.Files {
		lock, err := os.OpenFile(file.Path+".skygo-admin.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		err = func() error {
			defer lock.Close()
			if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
				return errors.New("environment locked")
			}
			defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			raw, err := a.normalizedEnv(file)
			if err != nil {
				return err
			}
			_, info, err := readEnv(file.Path)
			if err != nil {
				return err
			}
			if !restore {
				for _, edit := range file.Edits {
					raw = replaceLine(raw, edit.Key, edit.Target, true)
				}
			}
			return writeEnv(file.Path, raw, info)
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
func (a *Agent) revisionForTask(s LocalService, id string) (string, error) {
	j, err := a.readEnvJournal(id)
	if os.IsNotExist(err) {
		return a.unitRevision(s)
	}
	if err != nil {
		return "", err
	}
	overrides := map[string][]byte{}
	for _, f := range j.Files {
		raw, err := a.normalizedEnv(f)
		if err != nil {
			return "", err
		}
		overrides[f.Path] = raw
	}
	return a.unitRevisionWithEnv(s, overrides)
}

func onlyServiceImageChanged(before, after []byte, service string) bool {
	var a, b map[string]any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		return false
	}
	left, ok := a["services"].(map[string]any)
	if !ok {
		return false
	}
	right, ok := b["services"].(map[string]any)
	if !ok {
		return false
	}
	x, ok := left[service].(map[string]any)
	if !ok {
		return false
	}
	y, ok := right[service].(map[string]any)
	if !ok {
		return false
	}
	x["image"] = y["image"]
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	return bytes.Equal(rawA, rawB)
}

// Completion verifies the expected committed state; it does not silently rewrite
// an operator's later change merely because the old value was once authorized.
func (a *Agent) envMatches(id string, target bool) bool {
	j, err := a.readEnvJournal(id)
	if err != nil {
		return false
	}
	for _, file := range j.Files {
		raw, _, err := readEnv(file.Path)
		if err != nil {
			return false
		}
		original, err := a.normalizedEnv(file)
		if err != nil {
			return false
		}
		expected := original
		if target {
			for _, edit := range file.Edits {
				expected = replaceLine(expected, edit.Key, edit.Target, true)
			}
		}
		if !bytes.Equal(raw, expected) {
			return false
		}
	}
	return true
}
