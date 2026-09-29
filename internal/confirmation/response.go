package confirmation

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// Replay responses can contain a one-time node credential. Never persist them
// as plaintext in the confirmation journal.
func responseCipher(secret []byte) (cipher.AEAD, error) {
	if len(secret) < 32 {
		return nil, errors.New("confirmation secret unavailable")
	}
	key := sha256.Sum256(append([]byte("skygo-admin-confirmation-response-v1:"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func SealResponse(secret []byte, id, body string) (string, error) {
	aead, err := responseCipher(secret)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(body), []byte(id))
	return "v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}
func openResponse(secret []byte, id, value string) (string, error) {
	aead, err := responseCipher(secret)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(value, "v1:") {
		return "", errors.New("invalid confirmation response")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	if err != nil || len(raw) < aead.NonceSize() {
		return "", errors.New("invalid confirmation response")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(id))
	if err != nil {
		return "", errors.New("confirmation response authentication failed")
	}
	return string(plain), nil
}
