package control

// Archive is an authenticated, immutable task attachment, never a URL or path.
type Archive struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	ImageID string `json:"image_id"`
}
type Progress struct {
	TaskID     string `json:"task_id"`
	Attempt    uint64 `json:"attempt"`
	Sequence   uint64 `json:"sequence"`
	Phase      string `json:"phase"`
	Bytes      int64  `json:"bytes"`
	Total      int64  `json:"total"`
	Reused     int64  `json:"reused"`
	Downloaded *int64 `json:"downloaded,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
type Cleanup struct {
	Kind     string `json:"kind"`
	SHA256   string `json:"sha256,omitempty"`
	ImageID  string `json:"image_id"`
	Platform string `json:"platform"`
}
