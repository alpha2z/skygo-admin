package app

import (
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// Undelivered commands can expire safely. Once delivery may have happened, retain
// the service lock and command identity until the agent supplies a receipt.
func (s *Server) expireTasks(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lock AuditLock
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, 1).Error; err != nil {
			return err
		}
		var tasks []Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status IN ? AND expires_at <= ?", []string{"pending", "queued", "dispatched"}, time.Now().UTC()).Limit(200).Find(&tasks).Error; err != nil {
			return err
		}
		for _, task := range tasks {
			if task.Status == "dispatched" {
				task.Status = "uncertain"
			} else {
				task.Status = "expired"
				if err := tx.Model(&ServiceRecord{}).Where("id = ? AND busy_task = ?", task.ServiceID, task.ID).Update("busy_task", "").Error; err != nil {
					return err
				}
			}
			if err := tx.Save(&task).Error; err != nil {
				return err
			}
			if err := s.appendAudit(tx, 0, "task.timeout", task.ID, map[string]string{"status": task.Status}); err != nil {
				return err
			}
		}
		return nil
	})
}
