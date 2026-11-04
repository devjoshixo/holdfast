package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/retr0/holdfast/internal/booking"
)

type HoldRequest struct {
	Customer string `json:"customer"`
}

type HoldResponse struct {
	Status    string    `json:"status"`
	EventID   int64     `json:"event_id"`
	SeatID    int64     `json:"seat_id"`
	HeldUntil time.Time `json:"held_until,omitempty"`
}

// handleHoldSeat processes POST /api/v1/events/{event_id}/seats/{seat_id}/hold
func (s *Server) handleHoldSeat(w http.ResponseWriter, r *http.Request) {
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

	var req HoldRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Customer == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must include a non-empty customer")
		return
	}

	res, err := booking.HoldSeat(r.Context(), s.pool, eventID, seatID, req.Customer, idempotencyKey)
	if err != nil || res.Outcome == booking.OutcomeSystemError {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to process hold request")
		return
	}

	switch res.Outcome {
	case booking.OutcomeHoldGranted:
		writeJSON(w, http.StatusCreated, HoldResponse{
			Status:    string(res.Outcome),
			EventID:   eventID,
			SeatID:    seatID,
			HeldUntil: res.HeldUntil,
		})
	case booking.OutcomeHoldActive:
		writeJSON(w, http.StatusOK, HoldResponse{
			Status:    string(res.Outcome),
			EventID:   eventID,
			SeatID:    seatID,
			HeldUntil: res.HeldUntil,
		})
	case booking.OutcomeSeatTaken:
		writeError(w, http.StatusConflict, "SEAT_TAKEN", "seat is already held or booked by another customer")
	case booking.OutcomeHoldExpired:
		writeError(w, http.StatusGone, "HOLD_EXPIRED", "previous hold expired before confirmation")
	case booking.OutcomeClientSideError:
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REUSED", "idempotency key was previously used with different parameters")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unhandled booking outcome")
	}
}
