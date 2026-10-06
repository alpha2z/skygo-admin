package plugin

import (
	"encoding/json"
	"time"
)

type PreparedImage struct {
	TaskID         string    `json:"task_id"`
	Service        string    `json:"service"`
	Host           string    `json:"host"`
	Image          string    `json:"image"`
	ImageID        string    `json:"image_id"`
	Platform       string    `json:"platform"`
	PluginRevision string    `json:"plugin_revision"`
	FinishedAt     time.Time `json:"finished_at"`
}

// WorkflowInventory is a core-provided snapshot, never a direct database handle.
type WorkflowInventory struct {
	Definition  json.RawMessage `json:"definition"`
	Observation json.RawMessage `json:"observation"`
}

// WorkflowPlanningRequest contains only locally authorized services. The result
// uses the public workflow plan contract; the core revalidates every target.
type WorkflowPlanningRequest struct {
	Prepared  []PreparedImage     `json:"prepared,omitempty"`
	RequestID string              `json:"request_id"`
	Provider  string              `json:"provider"`
	Intent    json.RawMessage     `json:"intent"`
	Inventory []WorkflowInventory `json:"inventory"`
}
