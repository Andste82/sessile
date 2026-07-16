// Package ws implements the WebSocket endpoint and per-client read/write pumps
// (PROJECT_PLAN.md §5).
package ws

// inboundControl is a client→server text control message. Currently only
// "resize" is defined.
type inboundControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
