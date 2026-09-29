package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func fixture() Manifest {
	return Manifest{Version: 1, ID: "ci-12-2", Build: Build{Repository: "example/tooling", Workflow: "build.yml", Ref: "refs/heads/main", SourceCommit: strings.Repeat("a", 40), RunID: 12, RunAttempt: 2}, Images: []Image{{Service: "admin-api", Platform: "linux/arm64", Reference: "example/api@sha256:" + strings.Repeat("b", 64)}}}
}
func TestBuildSignatureAndComponentBoundaries(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	m := fixture()
	signed, err := Sign(m, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(signed, []ed25519.PublicKey{pub}); err != nil {
		t.Fatal(err)
	}
	signed.Payload = append(signed.Payload, ' ')
	if _, err = Verify(signed, []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("tampered payload accepted")
	}
	m.Images[0].Service = "unapproved-component"
	if m.Validate() == nil {
		t.Fatal("non-public component accepted")
	}
	m = fixture()
	m.Images = append(m.Images, m.Images[0])
	if m.Validate() == nil {
		t.Fatal("duplicate role/platform accepted")
	}
	m = fixture()
	m.Build.RunAttempt++
	if m.Validate() == nil {
		t.Fatal("attempt identity mismatch accepted")
	}
}
