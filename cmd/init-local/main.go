// init-local writes fresh credentials to ignored private files, never stdout.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "runtime", "new private runtime directory")
	flag.Parse()
	path, err := filepath.Abs(*out)
	must(err)
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		panic("output already exists; initialization refuses to overwrite")
	}
	must(os.MkdirAll(filepath.Join(path, "secrets"), 0700))
	token := func() string {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		must(err)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	password, root := token(), token()
	values := map[string]string{"mysql-password": password, "mysql-root-password": root, "mysql-dsn": "admin:" + password + "@tcp(mysql:3306)/skygo_admin?parseTime=true&charset=utf8mb4&loc=UTC", "jwt": token(), "bootstrap": token(), "signing": base64.StdEncoding.EncodeToString(key), "signing.pub": base64.StdEncoding.EncodeToString(pub), "cluster": token()}
	for name, value := range values {
		must(settings.Atomic(filepath.Join(path, "secrets", name), []byte(value+"\n")))
	}
	cfg := map[string]any{"host_id": "local-host", "api_url": "http://127.0.0.1:18391", "token_file": filepath.Join(path, "secrets", "agent-token"), "public_key_file": filepath.Join(path, "secrets", "signing.pub"), "state_dir": filepath.Join(path, "agent"), "allow_loopback_http": true, "services": []any{}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	must(err)
	must(settings.Atomic(filepath.Join(path, "agent.json"), b))
	fmt.Println("Initialized private runtime files. No credentials printed. Enroll a host before starting the agent.")
}
func must(err error) {
	if err != nil {
		panic("local initialization failed")
	}
}
