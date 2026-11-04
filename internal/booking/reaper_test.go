package booking

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReapExpiredHolds_Basic(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatIDs := setupArena(t, pool, 3)
	expiredSeat := seatIDs[0]
	activeSeat := seatIDs[1]
	bookedSeat := seatIDs[2]

	// 1. Manually set seat states:
	// - expiredSeat: held, expired 10 seconds ago
	_, err = pool.Exec(ctx, `
		UPDATE seats 
		SET status = 'held', held_by = 'cust-expired', held_until = now() - interval '10 seconds'
		WHERE id = $1`, expiredSeat)
	if err != nil {
		t.Fatalf("setup expired seat: %v", err)
	}

	// - activeSeat: held, expires in 10 minutes
	_, err = pool.Exec(ctx, `
		UPDATE seats 
		SET status = 'held', held_by = 'cust-active', held_until = now() + interval '10 minutes'
		WHERE id = $1`, activeSeat)
	if err != nil {
		t.Fatalf("setup active seat: %v", err)
	}

	// - bookedSeat: booked
	_, err = pool.Exec(ctx, `
		UPDATE seats 
		SET status = 'booked'
		WHERE id = $1`, bookedSeat)
	if err != nil {
		t.Fatalf("setup booked seat: %v", err)
	}

	// 2. Run Reaper
	count, err := ReapExpiredHolds(ctx, pool, 100)
	if err != nil {
		t.Fatalf("reap holds failed: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 seat reaped, got %d", count)
	}

	// 3. Verify states in DB
	var s1Status string
	var s1HeldBy *string
	_ = pool.QueryRow(ctx, "SELECT status, held_by FROM seats WHERE id = $1", expiredSeat).Scan(&s1Status, &s1HeldBy)
	if s1Status != "free" || s1HeldBy != nil {
		t.Errorf("expected expiredSeat to be free, got status=%s, heldBy=%v", s1Status, s1HeldBy)
	}

	var s2Status string
	_ = pool.QueryRow(ctx, "SELECT status FROM seats WHERE id = $1", activeSeat).Scan(&s2Status)
	if s2Status != "held" {
		t.Errorf("expected activeSeat to remain held, got status=%s", s2Status)
	}

	var s3Status string
	_ = pool.QueryRow(ctx, "SELECT status FROM seats WHERE id = $1", bookedSeat).Scan(&s3Status)
	if s3Status != "booked" {
		t.Errorf("expected bookedSeat to remain booked, got status=%s", s3Status)
	}
	_ = eventID
}

func TestReapExpiredHolds_ConcurrentWorkersSkipLocked(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	totalSeats := 30
	_, seatIDs := setupArena(t, pool, totalSeats)

	// Set all 30 seats to expired holds
	for _, id := range seatIDs {
		_, err := pool.Exec(ctx, `
			UPDATE seats 
			SET status = 'held', held_by = 'someone', held_until = now() - interval '5 seconds'
			WHERE id = $1`, id)
		if err != nil {
			t.Fatalf("setup expired seat %d: %v", id, err)
		}
	}

	// Run 4 concurrent workers with small batch size (batch=5)
	workers := 4

	// Concurrency Tool 1: atomic.Int64
	// Multiple goroutines will add to this counter concurrently.
	// Atomic operations avoid data races without needing a sync.Mutex.
	var totalReaped atomic.Int64

	// Concurrency Tool 2: sync.WaitGroup
	// Tracks active workers. Main thread will wait until all 4 workers call wg.Done().
	var wg sync.WaitGroup
	wg.Add(workers)

	// Concurrency Tool 3: Channel Barrier (Starting Gun)
	// If you just launch `go func()`, goroutine 1 might start and finish before goroutine 4 even boots!
	// By having every worker wait on `<-startBarrier`, all 4 workers are queued up in memory.
	// The moment `close(startBarrier)` is called, Go's scheduler unblocks all 4 simultaneously!
	startBarrier := make(chan struct{})

	for i := 0; i < workers; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			// WAIT HERE: Block until the starting gun is fired by close(startBarrier)
			<-startBarrier

			workerReaped := int64(0)
			// Sweep loop: Keep asking for batches of 5 until no expired seats remain
			for {
				reaped, err := ReapExpiredHolds(ctx, pool, 5)
				if err != nil {
					t.Errorf("worker err: %v", err)
					return
				}

				workerReaped += reaped
				totalReaped.Add(reaped)

				// Termination condition: If 0 seats were reaped, the backlog is empty!
				if reaped == 0 {
					break
				}

				// Small yield to let other workers contend for row locks
				time.Sleep(5 * time.Millisecond)
			}
			t.Logf("Worker %d finished: reaped %d seats", workerID, workerReaped)
		}()
	}

	// FIRE THE STARTING GUN: Unblock all 4 worker goroutines at the exact same microsecond
	close(startBarrier)

	// WAIT: Block here until all 4 goroutines finish their sweep and call wg.Done()
	wg.Wait()

	if totalReaped.Load() != int64(totalSeats) {
		t.Errorf("expected exactly %d seats reaped across workers, got %d", totalSeats, totalReaped.Load())
	}

	// Check DB: all seats must now be 'free'
	var freeCount int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM seats WHERE status = 'free'").Scan(&freeCount)
	if freeCount != totalSeats {
		t.Errorf("expected all %d seats to be free, got %d", totalSeats, freeCount)
	}
}

// TestHoldSeat_ReaperVsConfirmRace tests the ultimate time race:
// Customer clicks Confirm at the exact moment the Reaper attempts to reclaim the seat.
func TestHoldSeat_ReaperVsConfirmRace(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	rounds := 20
	bookedCount := 0
	expiredCount := 0

	for round := 0; round < rounds; round++ {
		eventID, seatIDs := setupArena(t, pool, 1)
		seatID := seatIDs[0]
		customer := "racer-cust"
		reqID := fmt.Sprintf("req-race-%d", round)

		// 1. Customer places hold
		res, err := HoldSeat(ctx, pool, eventID, seatID, customer, reqID)
		if err != nil || res.Outcome != OutcomeHoldGranted {
			t.Fatalf("round %d hold failed: %v", round, err)
		}

		// 2. Alternate between razor-edge future (Confirm wins) and razor-edge past (Reaper wins)
		if round%2 == 0 {
			// 50ms in future: Customer has a chance to win the race
			_, err = pool.Exec(ctx, "UPDATE seats SET held_until = now() + interval '50 milliseconds' WHERE id = $1", seatID)
		} else {
			// Exactly now/past: Reaper has the edge
			_, err = pool.Exec(ctx, "UPDATE seats SET held_until = now() WHERE id = $1", seatID)
		}
		if err != nil {
			t.Fatalf("round %d expire seat: %v", round, err)
		}

		// 3. Race: Customer Confirm vs Reaper Sweep
		var wg sync.WaitGroup
		wg.Add(2)
		startBarrier := make(chan struct{})

		var confirmOutcome Outcome
		var reapedCount int64

		// Runner A: Customer Confirm
		go func() {
			defer wg.Done()
			<-startBarrier
			outcome, _ := ConfirmSeat(ctx, pool, eventID, seatID, customer, reqID+"-confirm")
			confirmOutcome = outcome
		}()

		// Runner B: Reaper Sweep
		go func() {
			defer wg.Done()
			<-startBarrier
			count, _ := ReapExpiredHolds(ctx, pool, 100)
			reapedCount = count
		}()

		close(startBarrier)
		wg.Wait()

		// 4. Invariant Check: Exactly one wins. Never both, never corrupted!
		var finalStatus string
		_ = pool.QueryRow(ctx, "SELECT status FROM seats WHERE id = $1", seatID).Scan(&finalStatus)

		if confirmOutcome == OutcomeBooked {
			bookedCount++
			if reapedCount != 0 {
				t.Fatalf("round %d: customer booked seat, but reaper also reaped it! reapedCount=%d", round, reapedCount)
			}
			if finalStatus != "booked" {
				t.Fatalf("round %d: customer outcome is booked, but DB status is %s", round, finalStatus)
			}
		} else if confirmOutcome == OutcomeHoldExpired || confirmOutcome == OutcomeSeatTaken {
			expiredCount++
			if finalStatus != "free" && finalStatus != "held" {
				t.Fatalf("round %d: customer expired, but DB status is %s", round, finalStatus)
			}
		} else {
			t.Fatalf("round %d: unexpected confirm outcome: %v", round, confirmOutcome)
		}
	}

	t.Logf("Race finished 20 rounds: %d booked, %d expired/reaped. Zero corrupted states.", bookedCount, expiredCount)
}
