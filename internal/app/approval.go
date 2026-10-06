package app

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// IndependentApprovalEnabled reports the policy for new operations. Existing
// records retain their policy across configuration changes and restarts.
func (s *Server) IndependentApprovalEnabled() bool { return s.cfg.IndependentApprovalEnabled }

func approvalIdentity(single bool, actor uint32) uint32 {
	if single {
		return 0
	}
	return actor
}

// cancelPending cannot cancel queued, dispatched or uncertain operations.
func (s *Server) cancelPending(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s.change(c, nil, kind+".cancel", c.Param("id"), func(tx *gorm.DB) (any, error) {
			var model any
			switch kind {
			case "task":
				model = &Task{}
			case "publication":
				model = &Publication{}
			case "workflow":
				model = &Workflow{}
			case "cleanup":
				model = &ImageCleanup{}
			default:
				return nil, errConflict
			}
			result := tx.Model(model).Where("id = ? AND requested_by = ? AND status = ?", c.Param("id"), c.GetUint("admin_id"), "pending").Update("status", "cancelled")
			if result.Error != nil {
				return nil, result.Error
			}
			if result.RowsAffected != 1 {
				return nil, errConflict
			}
			return gin.H{"id": c.Param("id"), "status": "cancelled"}, nil
		})
	}
}

// taskAuthority is rechecked before an ordinary task is authorized or signed.
func (s *Server) taskAuthority(tx *gorm.DB, t Task) error {
	if !s.publicationActor(tx, t.RequestedBy, "ops.write") {
		return errConflict
	}
	if t.Action == "configure" && !s.publicationActor(tx, t.RequestedBy, "config.write") {
		return errConflict
	}
	if t.Action == "logs" && !s.publicationActor(tx, t.RequestedBy, "ops.logs") {
		return errConflict
	}
	if !t.SingleConfirmation && t.ApprovedBy != 0 && (!s.publicationActor(tx, t.ApprovedBy, "ops.approve") || t.ApprovedBy == t.RequestedBy) {
		return errConflict
	}
	return nil
}
