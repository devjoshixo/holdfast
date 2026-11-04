package api

import (
	"encoding/json"
	"net/http"
)

// ErrorDetail defines the standard error schema returned to clients.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse wraps ErrorDetail for a consistent JSON root envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// writeJSON serializes any payload as JSON with appropriate content-type headers.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a standardized error envelope matching Google AIP/RFC 7807 conventions.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}
