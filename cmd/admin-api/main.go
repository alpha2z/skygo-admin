package main

import (
	"context"
	"flag"
	"github.com/alpha2z/skygo-admin/internal/app"
	skyapp "github.com/scott4game/skygo/app"
	"log"
	"time"
)

func main() {
	migrate := flag.Bool("migrate", false, "explicitly initialize or migrate the management schema")
	flag.Parse()
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
