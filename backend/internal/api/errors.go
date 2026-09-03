package api

import "github.com/gin-gonic/gin"

// errorBody is the unified error envelope (PROJECT_PLAN.md §6):
//
//	{"error":{"code":"…","message":"…"}}
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Common error codes.
const (
	CodeValidation = "validation"
	CodeNotFound   = "not_found"
	CodeConflict   = "conflict"
	CodeInternal   = "internal"
	// CodeUnavailable is for work refused because the server is going away,
	// not because the request was wrong.
	CodeUnavailable = "unavailable"
	// CodeAlreadyRunning narrows the conflict on a restart: the session is
	// running, so there is nothing to start. With several browsers on one
	// session this is a race one of them always loses, and it is not a failure
	// — the client that lost it wanted a live session and there is one, so it
	// needs to tell this apart from the other conflicts and just reconnect.
	CodeAlreadyRunning = "already_running"
	// CodeUnauthorized is for a missing or invalid session cookie (§10).
	CodeUnauthorized = "unauthorized"
	// CodeForbidden is for a valid session that isn't allowed to do this —
	// an admin-only route hit by a non-admin, for example.
	CodeForbidden = "forbidden"
	// CodeExchangeFailed is for a key-exchange attempt (§4.5.2) that reached
	// the host but failed there — wrong credentials, or the remote command
	// that appends authorized_keys itself failed.
	CodeExchangeFailed = "exchange_failed"
)

// respondError writes a JSON error envelope with the given HTTP status.
func respondError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{Code: code, Message: message}})
}
