// Package api implements the HTTP layer for the control plane.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"servercli/internal/logger"
	"servercli/internal/security"
	"servercli/internal/service"
)

const maxBodyBytes = 4 << 20 // 4 MiB

// errorBody is the uniform error envelope.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a uniform error response. Internal errors are logged and
// never leak details to the client.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, code, message string, details map[string]any) {
	body := errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: logger.RequestID(r.Context()),
		Details:   details,
	}}
	writeJSON(w, status, body)
}

// writeServiceError maps service sentinel errors to HTTP responses.
func writeServiceError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var status int
	var code string
	switch {
	case errors.Is(err, service.ErrNotAuthenticated):
		status, code = http.StatusUnauthorized, "UNAUTHENTICATED"
	case errors.Is(err, service.ErrInvalidCredentials):
		status, code = http.StatusUnauthorized, "INVALID_CREDENTIALS"
	case errors.Is(err, service.ErrLocked):
		status, code = http.StatusTooManyRequests, "ACCOUNT_LOCKED"
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrDisabled):
		status, code = http.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, service.ErrNotFound):
		status, code = http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, service.ErrConflict):
		status, code = http.StatusConflict, "CONFLICT"
	case errors.Is(err, service.ErrAmbiguous):
		status, code = http.StatusConflict, "AMBIGUOUS_SELECTOR"
	case errors.Is(err, service.ErrTerminal):
		status, code = http.StatusConflict, "TERMINAL_STATE"
	case errors.Is(err, service.ErrOffline):
		status, code = http.StatusConflict, "NODE_OFFLINE"
	case errors.Is(err, service.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "UNAVAILABLE"
	default:
		// Unknown internal error: never leak details.
		log.Error("internal error", "error", err, "request_id", logger.RequestID(r.Context()))
		status, code = http.StatusInternalServerError, "INTERNAL_ERROR"
	}
	writeError(w, r, log, status, code, err.Error(), nil)
}

// decodeJSON reads and validates a JSON body, returning a friendly error.
func decodeJSON(w http.ResponseWriter, r *http.Request, log *slog.Logger, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			writeError(w, r, log, http.StatusBadRequest, "BAD_REQUEST", "request body required", nil)
			return false
		}
		writeError(w, r, log, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body", nil)
		return false
	}
	return true
}

// randomID returns a short random hex identifier for request IDs.
func randomID() string {
	tok, err := security.NewToken(8)
	if err != nil {
		return "unknown"
	}
	return tok
}
