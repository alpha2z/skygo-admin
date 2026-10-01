package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskRequestIdentitySurvivesRetryMySQL(t *testing.T) {
	db, cfg := releaseDB(t)
	s, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Router()
	password := "synthetic-task-passphrase"
	check(t, call(r, nil, "POST", "/api/v1/bootstrap", map[string]string{"username": "owner", "email": "owner@example.com", "password": password}, map[string]string{"X-Bootstrap-Token": cfg.BootstrapToken}), 201)
	owner := signIn(t, s, r, "owner", password)
	check(t, call(r, owner, "POST", "/api/v1/hosts", map[string]string{"id": "host"}, nil), 200)
	check(t, call(r, owner, "POST", "/api/v1/services", map[string]any{"id": "service", "host_id": "host", "image": "example/service@sha256:" + strings.Repeat("a", 64)}, nil), 200)
	if err := db.Model(&Host{}).Where("id = ?", "host").Update("observations", "[]").Error; err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"request_id": "stable-request", "service": "service", "action": "deploy", "image": "example/service@sha256:" + strings.Repeat("b", 64)}
	first := call(r, owner, "POST", "/api/v1/tasks", request, nil)
	check(t, first, 200)
	var original Task
	if json.Unmarshal(first.Body.Bytes(), &original) != nil || original.ID != "stable-request" {
		t.Fatal("request identity not persisted")
	}
	if db.Model(&Task{}).Where("id = ?", original.ID).Update("status", "succeeded").Error != nil {
		t.Fatal("fixture status")
	}
	again := call(r, owner, "POST", "/api/v1/tasks", request, nil)
	check(t, again, 200)
	var restored Task
	json.Unmarshal(again.Body.Bytes(), &restored)
	if restored.Status != "succeeded" || restored.ID != original.ID {
		t.Fatal("retry did not recover original result")
	}
	var count int64
	db.Model(&Task{}).Where("id = ?", original.ID).Count(&count)
	if count != 1 {
		t.Fatal("duplicate task")
	}
	request["image"] = "example/service@sha256:" + strings.Repeat("c", 64)
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 409)
	request["request_id"] = "invalid/id"
	check(t, call(r, owner, "POST", "/api/v1/tasks", request, nil), 400)
}
