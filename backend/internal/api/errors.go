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
)

// respondError writes a JSON error envelope with the given HTTP status.
func respondError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{Code: code, Message: message}})
}
