package app

import (
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"strings"
	"testing"
	"time"
)

func TestPluginInventoryRetainsOfflineAndStoppedServicesMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	cfg.PluginInventories = map[string][]string{"example": {"worker", "outside"}}
	image := "example/worker@sha256:" + strings.Repeat("a", 64)
	for _, id := range []string{"worker", "outside"} {
		plugin := "example"
		if id == "outside" {
			plugin = "other"
		}
		raw, _ := json.Marshal(control.Service{ID: id, HostID: "host", Image: image, PluginID: plugin})
		if err := db.Create(&ServiceRecord{ID: id, Definition: string(raw)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal([]control.Observation{{Service: "worker", Running: false, ObservationOnly: true}})
	db.Create(&Host{ID: "host", Active: false, LastSeen: time.Now().Add(-time.Hour), Observations: string(raw)})
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	check(t, call(r, nil, "GET", "/api/v1/plugins/example/inventory", nil, nil), 401)
	pw := "synthetic-inventory-password"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": pw}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", pw)
	w := call(r, owner, "GET", "/api/v1/plugins/example/inventory", nil, nil)
	check(t, w, 200)
	var result struct {
		Services []struct {
			Service     control.Service      `json:"service"`
			Online      bool                 `json:"online"`
			HostActive  bool                 `json:"host_active"`
			Observation *control.Observation `json:"observation"`
		} `json:"services"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Services) != 1 {
		t.Fatal("scope leaked or missing registered service")
	}
	row := result.Services[0]
	if row.Service.ID != "worker" || row.Online || row.HostActive || row.Observation == nil || row.Observation.Running {
		t.Fatal("offline/stopped status lost")
	}
	db.Model(&Host{}).Where("id = ?", "host").Update("observations", "[]")
	w = call(r, owner, "GET", "/api/v1/plugins/example/inventory", nil, nil)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Services) != 1 || result.Services[0].Observation != nil {
		t.Fatal("missing observation deleted definition")
	}
}
