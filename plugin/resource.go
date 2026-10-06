package plugin

import "encoding/json"

// Actor contains only the identity and permission checked by the core for this
// request. It is not a credential and cannot authorize an unrelated request.
type Actor struct {
	ID         uint32 `json:"id"`
	Role       string `json:"role"`
	Permission string `json:"permission"`
}

// ResourceRequest is a bounded JSON resource call, not a reverse proxy. There
// are deliberately no cookies, authorization headers or client-supplied URLs.
type ResourceRequest struct {
	Actor       Actor               `json:"actor"`
	OperationID string              `json:"operation_id,omitempty"`
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Query       map[string][]string `json:"query,omitempty"`
	Body        json.RawMessage     `json:"body,omitempty"`
}
type ResourceResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}
