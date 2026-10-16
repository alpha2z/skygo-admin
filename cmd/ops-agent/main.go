package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/alpha2z/skygo-admin/internal/agent"
	skyapp "github.com/scott4game/skygo/app"
	"log"
	"os"
	"time"
)

func main() {
	path := flag.String("config", "", "operator-owned inventory JSON")
	updater := flag.Bool("updater", false, "run the independent Agent update worker")
	resolve := flag.String("resolve-failed", "", "after host inspection, mark one uncertain receipt failed; agent must be stopped")
	flag.Parse()
	b, err := os.ReadFile(*path)
	if err != nil {
		log.Fatal("inventory unavailable")
	}
	var cfg agent.Config
	if json.Unmarshal(b, &cfg) != nil {
		log.Fatal("invalid inventory")
	}
	if *updater {
		u, err := agent.NewUpdater(cfg)
		if err != nil {
			log.Fatal(err)
		}
		r := &skyapp.Runtime{}
		if r.Add("agent-updater", u) != nil || r.Run(context.Background(), 150*time.Second) != nil {
			log.Fatal("updater stopped with an error")
		}
		return
	}
	a, err := agent.New(cfg, agent.Docker{})
	if err != nil {
		log.Fatal(err)
	}
	if *resolve != "" {
		if a.ResolveFailed(*resolve) != nil {
			log.Fatal("operator resolution rejected; stop agent and inspect receipt")
		}
		log.Print("receipt marked failed; restart agent to deliver result")
		return
	}
	r := &skyapp.Runtime{}
	if r.Add("ops-agent", a) != nil {
		log.Fatal("component initialization failed")
	}
	if r.Run(context.Background(), 150*time.Second) != nil {
		log.Fatal("runtime stopped with an error")
	}
}
