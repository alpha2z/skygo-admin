package admin

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordInputConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		failure             bool
	}{{"match", "synthetic-password", "synthetic-password", false}, {"mismatch", "synthetic-password", "different-password", true}, {"short", "short", "short", true}, {"unicode", "测试口令测试口令测试口令", "测试口令测试口令测试口令", false}} {
		t.Run(tc.name, func(t *testing.T) {
			values := []string{tc.first, tc.second}
			n := 0
			password, err := confirmPassword(func(string) ([]byte, error) { v := []byte(values[n]); n++; return v, nil })
			defer clear(password)
			if (err != nil) != tc.failure {
				t.Fatalf("unexpected input result: %v", err)
			}
			if !tc.failure && string(password) != tc.first {
				t.Fatal("password was modified")
			}
		})
	}
	first := []byte("synthetic-password")
	calls := 0
	_, err := confirmPassword(func(string) ([]byte, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return nil, errors.New("cancelled")
	})
	if err == nil || !bytes.Equal(first, make([]byte, len(first))) {
		t.Fatal("cancelled input retained password")
	}
}
func TestPasswordFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "password")
	for _, tc := range []struct {
		name, value, want string
		invalid           bool
	}{{"newline", " synthetic-password \n", " synthetic-password ", false}, {"crlf", "synthetic-password\r\n", "synthetic-password", false}, {"multiline", "synthetic-password\nother", "", true}, {"large", strings.Repeat("s", 1027), "", true}, {"short", "short", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.value), 0600); err != nil {
				t.Fatal(err)
			}
			password, err := readPasswordFile(path)
			defer clear(password)
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected file result: %v", err)
			}
			if !tc.invalid && string(password) != tc.want {
				t.Fatal("file password changed")
			}
		})
	}
	if err := os.WriteFile(path, []byte("synthetic-password"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(path); err == nil {
		t.Fatal("public password file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := readPasswordFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var output bytes.Buffer
	if _, err := readNewPassword("", f, &output); err == nil || output.Len() != 0 {
		t.Fatal("nonterminal input accepted")
	}
}
