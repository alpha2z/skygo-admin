// baseline provides a root-operated, offline historical rollback enrollment tool.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/agent"
	"github.com/alpha2z/skygo-admin/internal/baseline"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"os"
	"path/filepath"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "capture", "capture or sign")
	config := flag.String("config", "", "local Agent config")
	service := flag.String("service", "", "local service ID")
	record := flag.String("record", "", "record JSON; capture output with archive metadata")
	directory := flag.String("directory", "", "protected baseline directory containing the checksum-named archive")
	key := flag.String("key", "", "management signing key file")
	flag.Parse()
	if *mode == "capture" {
		var cfg agent.Config
		b, e := os.ReadFile(*config)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &cfg); e != nil {
			return e
		}
		r, e := agent.CaptureBaseline(cfg, *service)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(r)
	}
	if *mode != "sign" {
		return fmt.Errorf("unsupported mode")
	}
	var r baseline.Record
	b, e := os.ReadFile(*record)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return e
	}
	if r.ConfigID == "" {
		r.ConfigID, e = baseline.ArchiveConfigID(filepath.Join(*directory, r.ArchiveSHA256+".tar"))
		if e != nil {
			return e
		}
	}
	if e = r.Validate(); e != nil {
		return e
	}
	if e = baseline.VerifyArchive(filepath.Join(*directory, r.ArchiveSHA256+".tar"), r); e != nil {
		return e
	}
	raw, e := settings.Secret(*key)
	if e != nil {
		return e
	}
	priv, e := base64.StdEncoding.DecodeString(raw)
	if e != nil || len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid signing key")
	}
	id, envelope, e := baseline.Sign(r, priv)
	if e != nil {
		return e
	}
	path := filepath.Join(*directory, id+".json")
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(e) {
		old, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if string(old) != string(envelope) {
			return fmt.Errorf("baseline conflict")
		}
		fmt.Println(id)
		return nil
	}
	if e != nil {
		return e
	}
	_, e = f.Write(envelope)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println(id)
	return nil
}
