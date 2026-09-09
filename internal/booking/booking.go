package booking

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcome is one of the nine results a booking attempt can produce.
// These map 1:1 to the outcomes in docs/M1-design.md.
type Outcome string

const (
	OutcomeBooked          Outcome = "booked"       // seat confirmed for this customer
	OutcomeSeatTaken       Outcome = "seat_taken"   // someone else already holds this seat
	OutcomeSoldOut         Outcome = "sold_out"     // no seats left at all (matters more for "any seat" bookings)
	OutcomeAlreadyMine     Outcome = "already_mine" // this exact request already succeeded — a retry (Guarantee 4)
	OutcomeSystemError     Outcome = "system_error" // something failed that isn't the caller's fault
	OutcomeClientSideError Outcome = "client_error" // same request_id reused for a different seat — a client bug
	OutcomeHoldGranted     Outcome = "hold_granted" // user got the hold for a seat with held_until attached
	OutcomeHoldActive      Outcome = "hold_active"  // customer already holds the seat and is under the held_until time limit
	OutcomeHoldExpired     Outcome = "hold_expired" // the customer's hold expired before they confirmed
)

// SeatStatus is the value stored in seats.status for mechanism 3 (CAS).
// Using these constants everywhere avoids ever hand-typing "free"/"booked"
// in a query — a typo there would silently break the compare-and-swap.
type SeatStatus string

const (
	SeatStatusFree   SeatStatus = "free"
	SeatStatusBooked SeatStatus = "booked"
	SeatStatusHeld   SeatStatus = "held"
)

// BookSeat attempts to claim one specific seat for one customer, mechanism 2:
// the UNIQUE(event_id, seat_id) constraint is what makes the claim atomic.
//
// The body is intentionally left for you to write. The logic you derived today:
//  1. Look up requestID in bookings first. If a row already carries it, this is a
//     retry -> return that row's original outcome (OutcomeAlreadyMine). Do NOT insert.
//  2. Otherwise INSERT the booking.
//  3. If the INSERT fails with a unique violation (pgx error code "23505"),
//     the seat is already someone else's -> OutcomeSeatTaken.
//  4. Any other error -> OutcomeSystemError (with the real error for logging).
//
// To catch the unique violation you'll use, from "github.com/jackc/pgx/v5/pgconn":
//
//	var pgErr *pgconn.PgError
//	if errors.As(err, &pgErr) && pgErr.Code == "23505" { ... }
func BookSeat(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (Outcome, int, error) {
	var foundSeat int64
	err := pool.QueryRow(ctx, "SELECT seat_id FROM bookings WHERE request_id = $1", requestID).Scan(&foundSeat)
	if err == nil {
		return OutcomeAlreadyMine, 0, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return OutcomeSystemError, 0, err
	}

	_, err = pool.Exec(ctx, "INSERT INTO bookings (event_id, seat_id, customer, request_id) VALUES ($1, $2, $3, $4)", eventID, seatID, customer, requestID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return OutcomeSeatTaken, 0, nil
		}
		return OutcomeSystemError, 0, err
	}

	return OutcomeBooked, 0, nil
}

// BookSeatRowLock is mechanism 1: the pessimistic row lock.
// Where BookSeat (mechanism 2) lets losers collide at the INSERT and bounce off
// the unique constraint with an error, this makes the seat row EXCLUSIVE with
// SELECT ... FOR UPDATE. Losers QUEUE on the lock; when their turn comes they
// re-check under the lock, see the seat is gone, and get a clean OutcomeSeatTaken
// — no error, no wasted insert. Pessimistic (serialize) vs optimistic (collide).
//
// Your design from M1-design.md, in order:
//
//	BEGIN
//	  SELECT ... FROM seats WHERE id = seatID FOR UPDATE   -- acquire the lock
//	  check: is this seat already in bookings?
//	     found -> OutcomeSeatTaken (someone beat you; do NOT insert)
//	     none  -> INSERT the booking
//	COMMIT                                                  -- release the lock
func BookSeatRowLock(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (Outcome, int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return OutcomeSystemError, 0, err
	}
	defer tx.Rollback(ctx) // safety net; harmless no-op after a successful Commit

	// Step 1 — lock the seat row (seats table, FOR UPDATE, via tx). Just the lock.
	var lockedSeat int64
	err = tx.QueryRow(ctx, "SELECT id from seats WHERE id = $1 FOR UPDATE", seatID).Scan(&lockedSeat)
	if err != nil {
		return OutcomeSystemError, 0, err
	}

	var existingBooking int64
	err = tx.QueryRow(ctx, "SELECT id from bookings WHERE seat_id = $1", seatID).Scan(&existingBooking)
	if err == nil {
		return OutcomeSeatTaken, 0, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return OutcomeSystemError, 0, err
	}

	// Step 3 — seat is free: insert the booking (via tx).
	_, err = tx.Exec(ctx, "INSERT INTO bookings (event_id, seat_id, customer, request_id) VALUES ($1, $2, $3, $4)", eventID, seatID, customer, requestID)
	if err != nil {
		return OutcomeSystemError, 0, err
	}

	// Step 4 — commit releases the lock, then return booked.
	err = tx.Commit(ctx)
	if err != nil {
		return OutcomeSystemError, 0, err
	}

	return OutcomeBooked, 0, nil
}

// BookSeatCAS is mechanism 3. Graded submission — from your own Tuesday design, from memory.
// No step comments this time. Submit when ready; final.
func BookSeatCAS(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (Outcome, int, error) {
	retries := 0
	tx, err := pool.Begin(ctx)
	if err != nil {
		return OutcomeSystemError, retries, err
	}
	defer tx.Rollback(ctx) // safety net; harmless no-op after a successful Commit

	txc, txErr := tx.Begin(ctx)
	if txErr != nil {
		return OutcomeSystemError, retries, txErr
	}
	_, err = txc.Exec(ctx, "INSERT INTO request_lookup (request_id,seat_id) VALUES ($1,$2)", requestID, seatID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			var outcome string
			var storedSeatID int64
			txc.Rollback(ctx)
			err = tx.QueryRow(ctx, "SELECT outcome,seat_id FROM request_lookup WHERE request_id = $1", requestID).Scan(&outcome, &storedSeatID)
			if err != nil {
				return OutcomeSystemError, retries, err
			}
			if seatID != storedSeatID {
				return OutcomeClientSideError, retries, nil
			}
			if outcome == string(OutcomeBooked) {
				return OutcomeAlreadyMine, retries, nil
			}
			return Outcome(outcome), retries, nil
		} else {
			return OutcomeSystemError, retries, err
		}

	}
	commErr := txc.Commit(ctx)
	if commErr != nil {
		return OutcomeSystemError, retries, commErr
	}
	var freeSeat string
	for {
		err := tx.QueryRow(ctx, "SELECT status from seats WHERE id = $1", seatID).Scan(&freeSeat)
		if err != nil {
			return OutcomeSystemError, retries, err
		} else {
			if freeSeat == string(SeatStatusBooked) {
				_, err := tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeSeatTaken, requestID)
				if err != nil {
					return OutcomeSystemError, retries, err
				}
				err = tx.Commit(ctx)
				if err != nil {
					return OutcomeSystemError, retries, err
				}
				return OutcomeSeatTaken, retries, nil
			}
		}

		newSeat, err := tx.Exec(ctx, "UPDATE seats SET status = $1 WHERE id = $2 AND status = $3", SeatStatusBooked, seatID, SeatStatusFree)
		if err != nil {
			return OutcomeSystemError, retries, err
		}

		if newSeat.RowsAffected() > 0 {
			break
		}

		retries++

	}

	_, err = tx.Exec(ctx, "INSERT INTO bookings (event_id, seat_id, customer, request_id) VALUES ($1, $2, $3, $4)", eventID, seatID, customer, requestID)
	if err != nil {
		return OutcomeSystemError, retries, err
	}

	_, err = tx.Exec(ctx, "UPDATE request_lookup SET outcome = $1 WHERE request_id = $2", OutcomeBooked, requestID)
	if err != nil {
		return OutcomeSystemError, retries, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return OutcomeSystemError, retries, err
	}

	return OutcomeBooked, retries, nil
}
