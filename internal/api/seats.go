package api

import (
	"net/http"
	"strconv"
	"time"
)

type SeatView struct {
	ID        int64      `json:"id"`
	EventID   int64      `json:"event_id"`
	Label     string     `json:"label"`
	Status    string     `json:"status"`
	HeldBy    *string    `json:"held_by,omitempty"`
	HeldUntil *time.Time `json:"held_until,omitempty"`
}

// handleListSeats handles GET /api/v1/events/{event_id}/seats
func (s *Server) handleListSeats(w http.ResponseWriter, r *http.Request) {
	eventID, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_EVENT_ID", "event_id must be a positive integer")
		return
	}

	rows, err := s.pool.Query(r.Context(), `
		SELECT id, event_id, label, status, held_by, held_until,
		       held_until > now() AS is_live
		FROM seats
		WHERE event_id = $1
		ORDER BY id ASC
	`, eventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch seats")
		return
	}
	defer rows.Close()

	var seats []SeatView
	for rows.Next() {
		var seat SeatView
		var isLive *bool
		if err := rows.Scan(&seat.ID, &seat.EventID, &seat.Label, &seat.Status, &seat.HeldBy, &seat.HeldUntil, &isLive); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to scan seat row")
			return
		}

		// Re-gate on read: if marked held but expired past held_until, client sees status as free
		if seat.Status == "held" && isLive != nil && !*isLive {
			seat.Status = "free"
			seat.HeldBy = nil
			seat.HeldUntil = nil
		}

		seats = append(seats, seat)
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "error reading seat rows")
		return
	}

	if seats == nil {
		seats = []SeatView{}
	}

	writeJSON(w, http.StatusOK, seats)
}
