// sign-images signs an explicit image manifest with a separate CI build key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"github.com/alpha2z/skygo-admin/internal/release"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"log"
	"os"
	"path/filepath"
)

func main() {
	input := flag.String("manifest", "", "manifest JSON")
	keyFile := flag.String("key-file", "", "private base64 build signing key file")
	output := flag.String("out", "signed-images.json", "signed output")
	directory := flag.String("generate-dir", "", "create a new private directory containing a separate build key pair")
	flag.Parse()
	if *directory != "" {
		if os.Mkdir(*directory, 0700) != nil {
			log.Fatal("build key directory must not already exist")
		}
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal("key generation failed")
		}
		for name, value := range map[string][]byte{"signing": key, "signing.pub": pub} {
			if settings.Atomic(filepath.Join(*directory, name), []byte(base64.StdEncoding.EncodeToString(value)+"\n")) != nil {
				log.Fatal("key write failed")
			}
		}
		log.Print("Build keys generated in private files; no key material printed.")
		return
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		log.Fatal("manifest unavailable")
	}
	var m release.Manifest
	if json.Unmarshal(raw, &m) != nil {
		log.Fatal("invalid manifest")
	}
	secret, err := settings.Secret(*keyFile)
	if err != nil {
		log.Fatal("build key unavailable")
	}
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		log.Fatal("invalid build key")
	}
	signed, err := release.Sign(m, key)
	if err != nil {
		log.Fatal("manifest validation failed")
	}
	b, err := json.Marshal(signed)
	if err != nil || settings.Atomic(*output, b) != nil {
		log.Fatal("signed manifest write failed")
	}
}
