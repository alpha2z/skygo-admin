package app

import (
	"encoding/json"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/control"
	"strings"
	"testing"
	"time"
)

func TestTaskDetailSurvivesRecentWindowMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	router := s.Router()
	password := "synthetic-detail-password"
	check(t, call(router, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, router, "owner", password)
	result, _ := json.Marshal(control.Result{ID: "old-task", Status: "succeeded", Logs: "private diagnostic", Data: json.RawMessage(`{"receipt":"stable"}`)})
	rows := []Task{{ID: "old-task", Status: "succeeded", ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now().Add(-time.Hour), Payload: "{}", Result: string(result)}}
	for i := 0; i < 201; i++ {
		rows = append(rows, Task{ID: fmt.Sprintf("recent-%d", i), Status: "succeeded", ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(), Payload: "{}", Result: "{}"})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	check(t, call(router, nil, "GET", "/api/v1/tasks/old-task", nil, nil), 401)
	response := call(router, owner, "GET", "/api/v1/tasks/old-task", nil, nil)
	check(t, response, 200)
	if !strings.Contains(response.Body.String(), "stable") {
		t.Fatal("durable receipt unavailable")
	}
	check(t, call(router, owner, "GET", "/api/v1/tasks/missing-task", nil, nil), 404)
	check(t, call(router, owner, "POST", "/api/v1/admins", map[string]string{"Username": "viewer", "Email": "viewer@example.com", "Password": password, "Role": "viewer"}, nil), 200)
	viewer := signIn(t, s, router, "viewer", password)
	response = call(router, viewer, "GET", "/api/v1/tasks/old-task", nil, nil)
	check(t, response, 200)
	if strings.Contains(response.Body.String(), "private diagnostic") || !strings.Contains(response.Body.String(), "stable") {
		t.Fatal("log permission boundary or receipt changed")
	}
}
