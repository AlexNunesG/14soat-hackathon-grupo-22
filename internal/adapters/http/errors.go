package httpapi

import "github.com/gin-gonic/gin"

// ErrorCode is the machine-readable code of an error response
// (docs/openapi.yaml, components.schemas.ErrorCode). Clients branch on it.
type ErrorCode string

// The error codes of the v1 contract.
const (
	CodeInvalidRequest     ErrorCode = "invalid_request"
	CodeInvalidCredentials ErrorCode = "invalid_credentials" //nolint:gosec // G101: an error code, not a credential
	CodeUnauthorized       ErrorCode = "unauthorized"
	CodeEmailTaken         ErrorCode = "email_taken"
	CodeUnsupportedFormat  ErrorCode = "unsupported_format"
	CodeMissingFile        ErrorCode = "missing_file"
	CodeNotFound           ErrorCode = "not_found"
	CodeVideoNotReady      ErrorCode = "video_not_ready"
	CodePayloadTooLarge    ErrorCode = "payload_too_large"
	CodeInternal           ErrorCode = "internal"
)

// ErrorBody is the envelope of every 4xx/5xx response:
// {"error": {"code": "...", "message": "..."}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the content of ErrorBody.
type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// WriteError aborts the request with status and the error envelope.
func WriteError(c *gin.Context, status int, code ErrorCode, message string) {
	c.AbortWithStatusJSON(status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}
