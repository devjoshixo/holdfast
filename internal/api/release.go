package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/retr0/holdfast/internal/booking"
)

type ReleaseRequest struct {
	Customer string `json:"customer"`
}

type ReleaseResponse struct {
	Status  string `json:"status"`
	EventID int64  `json:"event_id"`
	SeatID  int64  `json:"seat_id"`
}

// handleReleaseHold processes POST /api/v1/events/{event_id}/seats/{seat_id}/release
func (s *Server) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	eventID, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_EVENT_ID", "event_id must be a positive integer")
		return
	}

	seatID, err := strconv.ParseInt(r.PathValue("seat_id"), 10, 64)
	if err != nil || seatID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_SEAT_ID", "seat_id must be a positive integer")
		return
	}

	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key header is required")
		return
	}

	var req ReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Customer == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must include a non-empty customer")
		return
	}

	outcome, err := booking.ReleaseHold(r.Context(), s.pool, eventID, seatID, req.Customer, idempotencyKey)
	if err != nil || outcome == booking.OutcomeSystemError {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to release hold")
		return
	}

	switch outcome {
	case booking.OutcomeHoldReleased:
		writeJSON(w, http.StatusOK, ReleaseResponse{
			Status:  string(outcome),
			EventID: eventID,
			SeatID:  seatID,
		})
	case booking.OutcomeClientSideError:
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REUSED", "idempotency key was previously used with different parameters")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unhandled booking outcome")
	}
}
