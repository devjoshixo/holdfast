package booking

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHoldSeat_LifecycleAndRetry(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatIDs := setupArena(t, pool, 1)
	seatID := seatIDs[0]

	customerA := "customer-A"
	customerB := "customer-B"
	reqIDA := "req-hold-A-123"
	reqIDB := "req-hold-B-456"

	// 1. Customer A holds free seat -> should get OutcomeHoldGranted
	resA, err := HoldSeat(ctx, pool, eventID, seatID, customerA, reqIDA)
	if err != nil {
		t.Fatalf("customer A hold failed with error: %v", err)
	}
	if resA.Outcome != OutcomeHoldGranted {
		t.Errorf("expected Customer A outcome %q, got %q", OutcomeHoldGranted, resA.Outcome)
	}
	if resA.HeldUntil.Before(time.Now()) {
		t.Errorf("expected HeldUntil to be in future, got %v", resA.HeldUntil)
	}

	// 2. Customer B tries to hold the same seat -> should get OutcomeSeatTaken
	resB, err := HoldSeat(ctx, pool, eventID, seatID, customerB, reqIDB)
	if err != nil {
		t.Fatalf("customer B hold failed with error: %v", err)
	}
	if resB.Outcome != OutcomeSeatTaken {
		t.Errorf("expected Customer B outcome %q, got %q", OutcomeSeatTaken, resB.Outcome)
	}

	// 3. Customer A retries with same requestID -> should return their existing active hold
	retryA, err := HoldSeat(ctx, pool, eventID, seatID, customerA, reqIDA)
	if err != nil {
		t.Fatalf("customer A retry failed with error: %v", err)
	}
	if retryA.Outcome != OutcomeHoldActive {
		t.Errorf("expected Customer A retry outcome %q, got %q", OutcomeHoldActive, retryA.Outcome)
	}
}

func TestConfirmSeat_SuccessAndRetry(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatIDs := setupArena(t, pool, 1)
	seatID := seatIDs[0]

	customer := "customer-A"
	holdReqID := "req-hold-1"
	confirmReqID := "req-confirm-1"

	// 1. Hold the seat first
	resHold, err := HoldSeat(ctx, pool, eventID, seatID, customer, holdReqID)
	if err != nil || resHold.Outcome != OutcomeHoldGranted {
		t.Fatalf("setup hold failed: %v, outcome: %q", err, resHold.Outcome)
	}

	// 2. Confirm the held seat -> OutcomeBooked
	outcome, err := ConfirmSeat(ctx, pool, eventID, seatID, customer, confirmReqID)
	if err != nil {
		t.Fatalf("confirm seat failed: %v", err)
	}
	if outcome != OutcomeBooked {
		t.Errorf("expected %q, got %q", OutcomeBooked, outcome)
	}

	// 3. Retry the same confirm request -> OutcomeBooked (idempotent)
	retryOutcome, err := ConfirmSeat(ctx, pool, eventID, seatID, customer, confirmReqID)
	if err != nil {
		t.Fatalf("confirm retry failed: %v", err)
	}
	if retryOutcome != OutcomeBooked {
		t.Errorf("expected retry %q, got %q", OutcomeBooked, retryOutcome)
	}

	// 4. Another user tries to confirm -> OutcomeSeatTaken
	otherOutcome, err := ConfirmSeat(ctx, pool, eventID, seatID, "customer-B", "req-confirm-2")
	if err != nil {
		t.Fatalf("other confirm failed: %v", err)
	}
	if otherOutcome != OutcomeSeatTaken {
		t.Errorf("expected %q for other customer, got %q", OutcomeSeatTaken, otherOutcome)
	}
}

func TestReleaseHold_Lifecycle(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatIDs := setupArena(t, pool, 1)
	seatID := seatIDs[0]

	customerA := "customer-A"
	customerB := "customer-B"

	// 1. Customer A holds free seat -> OutcomeHoldGranted
	resHold, err := HoldSeat(ctx, pool, eventID, seatID, customerA, "req-hold-A-1")
	if err != nil || resHold.Outcome != OutcomeHoldGranted {
		t.Fatalf("setup hold failed: %v, outcome: %q", err, resHold.Outcome)
	}

	// 2. Customer A releases hold early -> OutcomeHoldReleased
	outcomeRel, err := ReleaseHold(ctx, pool, eventID, seatID, customerA, "req-rel-A-1")
	if err != nil {
		t.Fatalf("release hold failed: %v", err)
	}
	if outcomeRel != OutcomeHoldReleased {
		t.Errorf("expected outcome %q, got %q", OutcomeHoldReleased, outcomeRel)
	}

	// 2b. Retry with same requestID -> should hit request_lookup and return OutcomeHoldReleased
	outcomeRetry, err := ReleaseHold(ctx, pool, eventID, seatID, customerA, "req-rel-A-1")
	if err != nil {
		t.Fatalf("release retry failed: %v", err)
	}
	if outcomeRetry != OutcomeHoldReleased {
		t.Errorf("expected retry outcome %q, got %q", OutcomeHoldReleased, outcomeRetry)
	}

	// 3. Customer B can now immediately hold the freed seat -> OutcomeHoldGranted
	resHoldB, err := HoldSeat(ctx, pool, eventID, seatID, customerB, "req-hold-B-1")
	if err != nil {
		t.Fatalf("customer B hold failed: %v", err)
	}
	if resHoldB.Outcome != OutcomeHoldGranted {
		t.Errorf("expected Customer B to get %q, got %q", OutcomeHoldGranted, resHoldB.Outcome)
	}

	// 4. Customer A tries to release seat again (stale release) -> returns OutcomeHoldReleased without touching Customer B's hold
	outcomeRelStale, err := ReleaseHold(ctx, pool, eventID, seatID, customerA, "req-rel-A-2")
	if err != nil {
		t.Fatalf("stale release failed: %v", err)
	}
	if outcomeRelStale != OutcomeHoldReleased {
		t.Errorf("expected %q, got %q", OutcomeHoldReleased, outcomeRelStale)
	}

	// 5. Customer B confirms their hold -> OutcomeBooked (proves Customer B was not displaced)
	confirmOutcome, err := ConfirmSeat(ctx, pool, eventID, seatID, customerB, "req-confirm-B-1")
	if err != nil {
		t.Fatalf("confirm seat failed: %v", err)
	}
	if confirmOutcome != OutcomeBooked {
		t.Errorf("expected %q, got %q", OutcomeBooked, confirmOutcome)
	}
}

func TestHoldSeat_ConcurrentExpiryRace(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatIDs := setupArena(t, pool, 1)
	seatID := seatIDs[0]

	// 1. Customer A holds seat initially
	customerA := "customer-A"
	resA, err := HoldSeat(ctx, pool, eventID, seatID, customerA, "req-hold-A")
	if err != nil || resA.Outcome != OutcomeHoldGranted {
		t.Fatalf("setup initial hold failed: %v, outcome: %q", err, resA.Outcome)
	}

	// 2. Set held_until to 15ms in the future so it's right on the brink of expiry
	_, err = pool.Exec(ctx, "UPDATE seats SET held_until = now() + INTERVAL '15 milliseconds' WHERE id = $1", seatID)
	if err != nil {
		t.Fatalf("failed to skew hold expiry: %v", err)
	}

	// 3. Setup concurrent racers:
	// - Customer A tries to confirm late
	// - 10 Snipers (Customer B0..B9) try to claim the expiring seat
	numSnipers := 10
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	type raceResult struct {
		customer string
		action   string
		outcome  Outcome
		err      error
	}

	results := make(chan raceResult, numSnipers+1)

	// Customer A racer
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		outcome, err := ConfirmSeat(ctx, pool, eventID, seatID, customerA, "req-confirm-A")
		results <- raceResult{customer: customerA, action: "confirm", outcome: outcome, err: err}
	}()

	// Snipers racers
	for i := 0; i < numSnipers; i++ {
		wg.Add(1)
		cust := fmt.Sprintf("sniper-%d", i)
		req := fmt.Sprintf("req-sniper-%d", i)
		go func(c, r string) {
			defer wg.Done()
			<-startBarrier
			holdRes, err := HoldSeat(ctx, pool, eventID, seatID, c, r)
			results <- raceResult{customer: c, action: "hold", outcome: holdRes.Outcome, err: err}
		}(cust, req)
	}

	// Fire the starting gun
	close(startBarrier)
	wg.Wait()
	close(results)

	// 4. Analyze results
	var bookedCount int
	var holdGrantedCount int
	var seatTakenCount int
	var holdExpiredCount int

	for res := range results {
		if res.err != nil {
			t.Errorf("racer %s (%s) failed with unexpected error: %v", res.customer, res.action, res.err)
		}
		switch res.outcome {
		case OutcomeBooked:
			bookedCount++
		case OutcomeHoldGranted:
			holdGrantedCount++
		case OutcomeSeatTaken:
			seatTakenCount++
		case OutcomeHoldExpired:
			holdExpiredCount++
		default:
			t.Errorf("racer %s returned unexpected outcome: %q", res.customer, res.outcome)
		}
	}

	// 5. Invariant Assertions:
	// Invariant 1: Mutual Exclusion. Either Customer A booked it, OR one sniper held it, but NEVER both.
	if bookedCount > 1 {
		t.Errorf("invariant violated: expected at most 1 booked, got %d", bookedCount)
	}
	if holdGrantedCount > 1 {
		t.Errorf("invariant violated: expected at most 1 sniper hold granted, got %d", holdGrantedCount)
	}
	if bookedCount == 1 && holdGrantedCount > 0 {
		t.Errorf("invariant violated: seat was both booked (%d) and held (%d)", bookedCount, holdGrantedCount)
	}

	// Invariant 2: Exactly 1 winner across all racers
	totalSuccesses := bookedCount + holdGrantedCount
	if totalSuccesses != 1 {
		t.Errorf("expected exactly 1 winner across all racers, got %d (booked: %d, held: %d)", totalSuccesses, bookedCount, holdGrantedCount)
	}

	// 6. Database Ground Truth Check: verify the database row matches our winner
	var dbStatus string
	var dbHeldBy *string
	err = pool.QueryRow(ctx, "SELECT status, held_by FROM seats WHERE id = $1", seatID).Scan(&dbStatus, &dbHeldBy)
	if err != nil {
		t.Fatalf("failed to query final seat state: %v", err)
	}

	if bookedCount == 1 {
		if dbStatus != string(SeatStatusBooked) {
			t.Errorf("expected db status %q, got %q", SeatStatusBooked, dbStatus)
		}
	} else if holdGrantedCount == 1 {
		if dbStatus != string(SeatStatusHeld) {
			t.Errorf("expected db status %q, got %q", SeatStatusHeld, dbStatus)
		}
		if dbHeldBy == nil || *dbHeldBy == customerA {
			t.Errorf("expected seat held_by to be a winning sniper, got %v", dbHeldBy)
		}
	}
}
