package plugin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestTrustAndCapabilities(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := Manifest{Protocol: 1, CoreProtocol: 1, ID: "example", Version: "1.0.0", Capabilities: []string{"observe", "maintain"}}
	p, _ := json.Marshal(m)
	b, _ := json.Marshal(SignedManifest{Payload: p, Signature: ed25519.Sign(key, p)})
	got, rev, err := VerifyManifest(b, pub)
	if err != nil || len(rev) != 64 {
		t.Fatal(err)
	}
	granted, err := GrantedCapabilities(got, []string{"observe"})
	if err != nil || len(granted) != 1 {
		t.Fatal("unexpected privilege expansion")
	}
	if _, err := GrantedCapabilities(got, []string{"shell"}); err == nil {
		t.Fatal("undeclared permission accepted")
	}
	b[len(b)/2] ^= 1
	if _, _, err := VerifyManifest(b, pub); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}
func TestAssetIntegrity(t *testing.T) {
	dir := t.TempDir()
	b := []byte("export const version = 1;")
	sum := sha256.Sum256(b)
	p := filepath.Join(dir, "page.js")
	os.WriteFile(p, b, 0600)
	m := Manifest{Assets: map[string]string{"page.js": hex.EncodeToString(sum[:])}}
	if _, err := ReadAssets(dir, m); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("modified"), 0600)
	if _, err := ReadAssets(dir, m); err == nil {
		t.Fatal("modified asset accepted")
	}
	os.Remove(p)
	os.Symlink("/etc/passwd", p)
	if _, err := ReadAssets(dir, m); err == nil {
		t.Fatal("symlink accepted")
	}
}
