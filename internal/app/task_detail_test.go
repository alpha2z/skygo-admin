package app

import (
	"encoding/json"
	"fmt"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/gin-gonic/gin"
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

func TestTaskExtensionSummaryRequiresActionPermission(t *testing.T) {
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		t.Fatal(err)
	}
	auth.AddPolicy("operator", "sample.write")
	auth.AddPolicy("approver", "sample.approve")
	s := &Server{auth: auth, cfg: Config{AgentActions: map[string]AgentAction{"sample": {Permission: "sample.write", ApprovalPermission: "sample.approve"}}}}
	raw, _ := json.Marshal(control.Command{Action: "extension", Extension: "sample", Payload: json.RawMessage(`{"target":"reviewable"}`), Config: json.RawMessage(`{"internal":"never-return"}`)})
	task := Task{Action: "extension", Payload: string(raw)}
	for _, role := range []string{"operator", "approver", "viewer"} {
		context := &gin.Context{}
		context.Set("admin_role", role)
		view := s.taskResponse(context, task)
		b, _ := json.Marshal(view)
		if strings.Contains(string(b), "never-return") || strings.Contains(string(b), `"payload":`) {
			t.Fatal("raw command leaked")
		}
		if (len(view.ExtensionPayload) > 0) != (role != "viewer") || view.Extension != "sample" {
			t.Fatalf("incorrect summary for %s", role)
		}
	}
	task.Action = "configure"
	context := &gin.Context{}
	context.Set("admin_role", "operator")
	if s.taskResponse(context, task).ExtensionPayload != nil {
		t.Fatal("configuration payload exposed")
	}
}
