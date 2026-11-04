package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HoldResult struct {
	Outcome   Outcome
	HeldUntil time.Time
}

// HoldSeat attempts to place a temporary 10-minute hold on a seat for a customer.
// It checks request_lookup first for idempotency, verifies the seat is free or expired,
// and atomically updates the seat status to 'held' with held_until set.
func HoldSeat(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (HoldResult, error) {
	var heldUntil time.Time
	tx, err := pool.Begin(ctx)
	if err != nil {
		return HoldResult{Outcome: OutcomeSystemError}, err
	}
	defer tx.Rollback(ctx)

	txc, err := tx.Begin(ctx)
	if err != nil {
		return HoldResult{Outcome: OutcomeSystemError}, err
	}

	_, err = txc.Exec(ctx, "INSERT INTO request_lookup (request_id,seat_id) VALUES ($1,$2)", requestID, seatID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			var storedSeatID int64
			var outcome string
			var heldValid bool
			var heldUntil *time.Time
			var heldBy *string
			var seatStatus string
			txc.Rollback(ctx)

			err = tx.QueryRow(ctx, "SELECT r.outcome,r.seat_id,s.held_by,s.status,s.held_until,COALESCE(s.held_until > now(), false) AS still_live FROM request_lookup r JOIN seats s ON s.id = r.seat_id WHERE r.request_id = $1", requestID).Scan(&outcome, &storedSeatID, &heldBy, &seatStatus, &heldUntil, &heldValid)
			if err != nil {
				return HoldResult{Outcome: OutcomeSystemError}, err
			}

			if storedSeatID != seatID {
				return HoldResult{Outcome: OutcomeClientSideError}, nil
			}

			if outcome == string(OutcomeHoldGranted) {
				if seatStatus == string(SeatStatusBooked) {
					if *heldBy == customer {

						return HoldResult{Outcome: OutcomeAlreadyMine}, nil
					} else {

						return HoldResult{Outcome: OutcomeSeatTaken}, nil
					}

				}

				if *heldBy == customer && heldValid {
					return HoldResult{Outcome: OutcomeHoldActive, HeldUntil: *heldUntil}, nil

				} else {
					return HoldResult{Outcome: OutcomeHoldExpired}, nil
				}
			}

			return HoldResult{Outcome: Outcome(outcome)}, nil
		} else {
			return HoldResult{Outcome: OutcomeSystemError}, err
		}
	}

	err = txc.Commit(ctx)
	if err != nil {
		return HoldResult{Outcome: OutcomeSystemError}, err
	}

	err = tx.QueryRow(ctx, "UPDATE seats SET status = 'held', held_until = now() + interval '10 minutes',held_by = $1 WHERE id = $2 AND (status='free' OR (status='held' AND held_until < now()) ) RETURNING held_until", customer, seatID).Scan(&heldUntil)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return HoldResult{Outcome: OutcomeSystemError}, err
		}

		_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeSeatTaken, requestID)
		if err != nil {
			return HoldResult{Outcome: OutcomeSystemError}, err
		}

		err = tx.Commit(ctx)
		if err != nil {
			return HoldResult{Outcome: OutcomeSystemError}, err
		}

		return HoldResult{Outcome: OutcomeSeatTaken}, nil
	}

	_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeHoldGranted, requestID)
	if err != nil {
		return HoldResult{Outcome: OutcomeSystemError}, err
	}
	err = tx.Commit(ctx)
	if err != nil {
		return HoldResult{Outcome: OutcomeSystemError}, err
	}

	return HoldResult{Outcome: OutcomeHoldGranted, HeldUntil: heldUntil}, nil
}

// ConfirmSeat converts an active hold into a permanent booking.
// It ensures idempotency via request_lookup and verifies the caller currently holds
// the unexpired seat before atomically updating status to 'booked' and inserting the booking record.
func ConfirmSeat(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (Outcome, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}
	defer tx.Rollback(ctx)

	txc, err := tx.Begin(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}

	_, err = txc.Exec(ctx, "INSERT into request_lookup (request_id,seat_id) VALUES ($1,$2)", requestID, seatID)
	if err != nil {
		var pgErr *pgconn.PgError

		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			var outcome string
			var storedSeatID int64
			txc.Rollback(ctx)

			err = tx.QueryRow(ctx, "SELECT outcome,seat_id FROM request_lookup WHERE request_id = $1", requestID).Scan(&outcome, &storedSeatID)

			if storedSeatID != seatID {
				return OutcomeClientSideError, nil
			}

			return Outcome(outcome), nil
		}
	}

	err = txc.Commit(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}

	tag, err := tx.Exec(ctx, "UPDATE seats SET status = 'booked' WHERE id = $1 AND status = 'held' AND held_by = $2 AND held_until > now()", seatID, customer)
	if err != nil {
		return OutcomeSystemError, err
	}

	if tag.RowsAffected() == 0 {
		var seatStatus string
		var heldBy *string
		var still_live bool
		err = tx.QueryRow(ctx, "SELECT status, held_by, COALESCE(held_until > now(), false) as still_live FROM seats WHERE id = $1", seatID).Scan(&seatStatus, &heldBy, &still_live)
		if err != nil {
			return OutcomeSystemError, err
		}

		outcome := OutcomeSeatTaken
		if (heldBy != nil && *heldBy == customer && !still_live) || seatStatus == string(SeatStatusFree) {
			outcome = OutcomeHoldExpired
		}

		_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", outcome, requestID)
		if err != nil {
			return OutcomeSystemError, nil
		}
		tx.Commit(ctx)
		return outcome, nil
	}

	_, err = tx.Exec(ctx, "INSERT INTO bookings (event_id,seat_id,customer,request_id) VALUES ($1,$2,$3,$4)", eventID, seatID, customer, requestID)
	if err != nil {
		return OutcomeSystemError, err
	}

	_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeBooked, requestID)
	if err != nil {
		return OutcomeSystemError, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}

	return OutcomeBooked, nil

}

// ReleaseHold voluntarily releases a customer's hold on a seat early.
// It atomically resets the seat to 'free' only if the customer still actively holds it,
// ensuring a stale release cannot affect another buyer's hold on the same seat.
func ReleaseHold(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (Outcome, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}
	defer tx.Rollback(ctx)

	txc, err := tx.Begin(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}

	_, err = txc.Exec(ctx, "INSERT INTO request_lookup (request_id, seat_id) VALUES ($1, $2)", requestID, seatID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			var outcome string
			var storedSeatID int64
			txc.Rollback(ctx)

			err = tx.QueryRow(ctx, "SELECT outcome, seat_id FROM request_lookup WHERE request_id = $1", requestID).Scan(&outcome, &storedSeatID)
			if err != nil {
				return OutcomeSystemError, err
			}

			if storedSeatID != seatID {
				return OutcomeClientSideError, nil
			}

			return Outcome(outcome), nil
		}
		return OutcomeSystemError, err
	}

	err = txc.Commit(ctx)
	if err != nil {
		return OutcomeSystemError, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE seats 
		SET status = 'free', 
		    held_by = NULL, 
		    held_until = NULL 
		WHERE id = $1 
		  AND status = 'held' 
		  AND held_by = $2`,
		seatID, customer,
	)
	if err != nil {
		return OutcomeSystemError, err
	}

	_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeHoldReleased, requestID)
	if err != nil {
		return OutcomeSystemError, err
	}

	if err := tx.Commit(ctx); err != nil {
		return OutcomeSystemError, err
	}

	return OutcomeHoldReleased, nil
}
