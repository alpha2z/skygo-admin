package app

import (
	"encoding/json"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/gin-gonic/gin"
	"time"
)

// Inventories are core-owned reads. Missing heartbeats never delete definitions.
func (s *Server) mountPluginInventories(a *gin.RouterGroup) {
	for id, ids := range s.cfg.PluginInventories {
		id, ids := id, append([]string(nil), ids...)
		a.GET("/plugins/"+id+"/inventory", s.require("ops.read"), func(c *gin.Context) {
			var records []ServiceRecord
			var hosts []Host
			if len(ids) == 0 {
				c.JSON(200, gin.H{"plugin_id": id, "services": []any{}})
				return
			}
			if s.db.Where("id IN ?", ids).Order("id").Find(&records).Error != nil || s.db.Find(&hosts).Error != nil {
				c.JSON(503, gin.H{"error": "inventory unavailable"})
				return
			}
			indexed := map[string]Host{}
			for _, h := range hosts {
				indexed[h.ID] = h
			}
			rows := []any{}
			for _, record := range records {
				var service control.Service
				if json.Unmarshal([]byte(record.Definition), &service) != nil {
					c.JSON(503, gin.H{"error": "invalid registered inventory"})
					return
				}
				if service.PluginID != id {
					continue
				}
				host, found := indexed[service.HostID]
				var observations []control.Observation
				valid := json.Unmarshal([]byte(host.Observations), &observations) == nil
				var observation *control.Observation
				if valid {
					for _, o := range observations {
						if o.Service == service.ID {
							o := o
							observation = &o
							break
						}
					}
				}
				online := found && host.Active && time.Since(host.LastSeen) >= -5*time.Second && time.Since(host.LastSeen) < time.Minute
				rows = append(rows, gin.H{"service": service, "busy_task": record.BusyTask, "host_registered": found, "host_active": host.Active, "last_seen": host.LastSeen, "online": online, "observation": observation})
			}
			c.JSON(200, gin.H{"plugin_id": id, "services": rows})
		})
	}
}
