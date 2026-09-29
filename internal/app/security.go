package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "s" + base64.RawURLEncoding.EncodeToString(value), nil
}
func hashSecret(secret string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(secret), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}
func verifySecret(encoded, secret string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false
	}
	memory, e1 := strconv.ParseUint(strings.TrimPrefix(params[0], "m="), 10, 32)
	iterations, e2 := strconv.ParseUint(strings.TrimPrefix(params[1], "t="), 10, 32)
	parallelism, e3 := strconv.ParseUint(strings.TrimPrefix(params[2], "p="), 10, 8)
	salt, e4 := base64.RawStdEncoding.DecodeString(parts[4])
	expected, e5 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || memory != 65536 || iterations != 3 || parallelism != 2 || len(salt) != 16 || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(secret), salt, uint32(iterations), uint32(memory), uint8(parallelism), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
func chainHash(previous string, operator uint32, action, target, detail string, at int64) string {
	value := fmt.Sprintf("%s|%d|%s|%s|%s|%d", previous, operator, action, target, detail, at)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
