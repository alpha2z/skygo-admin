package main

import (
	"context"
	"flag"
	"github.com/alpha2z/skygo-admin/admin"
	"github.com/alpha2z/skygo-admin/internal/app"
	skyapp "github.com/scott4game/skygo/app"
	"log"
	"time"
)

func main() {
	migrate := flag.Bool("migrate", false, "explicitly initialize or migrate the management schema")
	changePassword := flag.String("change-password", "", "change an existing administrator password and exit")
	passwordFile := flag.String("password-file", "", "read the new password from a private file instead of the terminal")
	flag.Parse()
	if flag.NArg() != 0 || (*migrate && *changePassword != "") || (*passwordFile != "" && *changePassword == "") {
		log.Fatal("invalid flag combination; use -help")
	}
	if *changePassword != "" {
		if err := admin.RunPasswordChange(*changePassword, *passwordFile, nil); err != nil {
			log.Fatal(err)
		}
		log.Print("administrator password changed; existing sessions revoked")
		return
	}
	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	db, err := app.OpenDB(cfg.MySQLDSN)
	if err != nil {
		log.Fatal(err)
	}
	sql, _ := db.DB()
	defer sql.Close()
	if *migrate {
		if app.Migrate(db) != nil {
			log.Fatal("migration failed")
		}
		log.Print("schema migrated")
		return
	}
	s, err := app.NewServer(cfg, db)
	if err != nil {
		log.Fatal(err)
	}
	r := &skyapp.Runtime{}
	if err = r.Add("admin-api", s); err != nil {
		log.Fatal("component initialization failed")
	}
	if r.Run(context.Background(), 20*time.Second) != nil {
		log.Fatal("runtime stopped with an error")
	}
}
