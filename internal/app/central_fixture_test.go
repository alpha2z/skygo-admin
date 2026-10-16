package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alpha2z/skygo-admin/internal/control"
)

func centralTestConfig(t *testing.T, s *Server) {
	t.Helper()
	s.distribution = &distributionConfig{Enabled: true, Directory: t.TempDir(), MaxBytes: 64 << 30, Username: "synthetic", TokenFile: "/unused/synthetic", Packages: []string{"example/api", "example/web", "example/worker", "example/service", "example/ops-agent"}}
}

// These state-machine tests substitute the archive producer. Byte verification is
// exercised by distribution_test and registrycache, not by this fixture.
func centralTestArchives(t *testing.T, s *Server, imageID string) {
	t.Helper()
	var tasks []Task
	if err := s.db.Where("action = ? AND status = ?", "prepare-image", "preparing").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		var c control.Command
		if json.Unmarshal([]byte(task.Payload), &c) != nil {
			t.Fatal("payload")
		}
		c.Archive = &control.Archive{SHA256: strings.Repeat("f", 64), Size: 1024, ImageID: imageID}
		raw, _ := json.Marshal(c)
		task.Payload = string(raw)
		task.PayloadHash = control.Digest(raw)
		task.Status = "queued"
		if err := s.db.Save(&task).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&ImageDelivery{}).Where("id = ?", task.DeliveryID).Updates(map[string]any{"status": "queued"}).Error; err != nil {
			t.Fatal(err)
		}
	}
}
func centralTestReceipt(t *testing.T, s *Server, host, service, image string) {
	t.Helper()
	centralTestConfig(t, s)
	id := "prepared-" + service
	now := time.Now().UTC()
	if err := s.db.Create(&Task{ID: id, HostID: host, ServiceID: service, Action: "prepare-image", Status: "succeeded", FinishedAt: &now, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&ImageDelivery{ID: id, TaskID: id, HostID: host, Service: service, Image: image, Mode: "archive", Status: "succeeded"}).Error; err != nil {
		t.Fatal(err)
	}
}
