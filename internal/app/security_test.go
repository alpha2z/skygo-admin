package app

import (
	"strings"
	"testing"
)

func TestArgon2IDPasswordRoundTrip(t *testing.T) {
	hash, err := hashSecret("a-strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if !verifySecret(hash, "a-strong-test-password") {
		t.Fatal("valid password rejected")
	}
	if verifySecret(hash, "wrong-password") {
		t.Fatal("invalid password accepted")
	}
}

func TestAuditChainHashChangesWithEveryField(t *testing.T) {
	base := chainHash("previous", 7, "deploy", "42", `{"reason":"maintenance"}`, 1000)
	values := []string{
		chainHash("changed", 7, "deploy", "42", `{"reason":"maintenance"}`, 1000),
		chainHash("previous", 8, "deploy", "42", `{"reason":"maintenance"}`, 1000),
		chainHash("previous", 7, "stop", "42", `{"reason":"maintenance"}`, 1000),
		chainHash("previous", 7, "deploy", "43", `{"reason":"maintenance"}`, 1000),
		chainHash("previous", 7, "deploy", "42", `{"reason":"other"}`, 1000),
		chainHash("previous", 7, "deploy", "42", `{"reason":"maintenance"}`, 1001),
	}
	for _, value := range values {
		if value == base {
			t.Fatal("audit hash did not bind all fields")
		}
	}
}

func TestAdminPasswordBeyondFormerReauthenticationLimit(t *testing.T) {
	password := strings.Repeat("long-passphrase-", 1000) + "末尾"
	hash, err := hashSecret(password)
	if err != nil {
		t.Fatal(err)
	}
	if !verifySecret(hash, password) {
		t.Fatal("long password rejected")
	}
	if verifySecret(hash, password+"changed") {
		t.Fatal("password was truncated")
	}
}
