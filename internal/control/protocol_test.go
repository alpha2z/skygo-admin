package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignaturesAndIdentity(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	c := Command{Version: 1, ID: "command-a", HostID: "host-a", Service: "worker", Action: "deploy", Image: "example/worker@sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	e, err := Sign(c, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(e, pub, "host-a"); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(e, pub, "host-b"); err == nil {
		t.Fatal("cross-host command accepted")
	}
	c.Image = "example/worker:latest"
	if c.Validate() == nil {
		t.Fatal("mutable image accepted")
	}
	e.Payload = json.RawMessage(strings.Replace(string(e.Payload), "worker", "other", 1))
	if _, err = Verify(e, pub, "host-a"); err == nil {
		t.Fatal("tampered command accepted")
	}
}
func TestDependencyGraph(t *testing.T) {
	image := "example/a@sha256:" + strings.Repeat("a", 64)
	s := []Service{{ID: "a", HostID: "host", Image: image}, {ID: "b", HostID: "host", Image: image, DependsOn: []string{"a"}}}
	if ValidateServices(s) != nil {
		t.Fatal("valid graph rejected")
	}
	s[0].DependsOn = []string{"b"}
	if ValidateServices(s) == nil {
		t.Fatal("cycle accepted")
	}
	s[0].DependsOn = []string{"missing"}
	if ValidateServices(s) == nil {
		t.Fatal("missing dependency accepted")
	}
}
