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

			err = tx.QueryRow(ctx, "SELECT r.outcome,r.seat_id,s.held_by,s.status,s.held_until,s.held_until > now() AS still_live FROM request_lookup r JOIN seats s ON s.id = r.seat_id WHERE r.request_id = $1", requestID).Scan(&outcome, &storedSeatID, &heldBy, &seatStatus, &heldUntil, &heldValid)
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

		var storedSeatID int64
		var outcome string
		var heldValid bool
		var heldUntil *time.Time
		var heldBy *string
		var seatStatus string

		err = tx.QueryRow(ctx, "SELECT r.outcome,r.seat_id,s.held_by,s.status,s.held_until,s.held_until > now() AS still_live FROM request_lookup r JOIN seats s ON s.id = r.seat_id WHERE r.request_id = $1", requestID).Scan(&outcome, &storedSeatID, &heldBy, &seatStatus, &heldUntil, &heldValid)
		if err != nil {
			return HoldResult{Outcome: OutcomeSystemError}, err
		}

		if seatStatus == string(SeatStatusBooked) {
			return HoldResult{Outcome: OutcomeBooked}, err
		}

	}

	_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeSeatTaken, requestID)
	if err != nil {
		return HoldResult{Outcome: OutcomeSystemError}, err
	}
	//put Update to the seats table for the seatID shared and check if it available to be put in held for someone else to take it
	//if the Returning gives back a row this means something was written and the seat is held
	//if not then we do a SELECT read nad give back a correct status of what is the state of the request, is it held by them already, someone else booked it

	panic("todo")
}
