package booking

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dsn returns the test database connection string.
func dsn() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://holdfast:holdfast_dev@localhost:5432/holdfast?pool_max_conns=90"
}

// setupArena wipes bookings and creates one event with exactly one seat.
// It returns the eventID and seatID that the test will hammer.
// This is the clean arena every concurrency run starts from.
func setupArena(t *testing.T, pool *pgxpool.Pool, seats int) (eventID int64, seatIDs []int64) {
	t.Helper()
	ctx := context.Background()
	seatIDs = []int64{}
	if _, err := pool.Exec(ctx, "TRUNCATE bookings, seats, events, request_lookup RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"INSERT INTO events (name, total_seats) VALUES ('racefest', $1) RETURNING id", seats).
		Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	for i := 0; i < seats; i++ {
		var id int64
		if err := pool.QueryRow(ctx,
			"INSERT INTO seats (event_id, label) VALUES ($1, $2) RETURNING id", eventID, fmt.Sprintf("seat-%d", i)).
			Scan(&id); err != nil {
			t.Fatalf("insert seat: %v", err)
		}

		seatIDs = append(seatIDs, id)
	}
	return eventID, seatIDs
}

// bookFunc is the shared signature of every mechanism, so the one harness can
// attack any of them (mechanism 2, mechanism 1, and CAS later).
type bookFunc func(ctx context.Context, pool *pgxpool.Pool, eventID, seatID int64, customer, requestID string) (Outcome, int, error)

type FlashSale struct {
	Outcome   Outcome
	totalTime time.Duration
	Batch     int
	retries   int
	seatIdx   int
}

// hammerOneSeat is the shootout judge: fire `attackers` goroutines at the SAME
// seat at the same instant using the given mechanism, then run the three checks.
// Only one may win, everyone else must lose cleanly, and the DB must agree.
func hammerOneSeat(t *testing.T, book bookFunc, seatCount int) {
	t.Helper()
	const attackers = 1000 * 30

	pool, err := pgxpool.New(context.Background(), dsn())
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	rangeLow := int(float64(seatCount) * 0.2)
	rangeHigh := int(float64(seatCount) * 0.8)

	eventID, seats := setupArena(t, pool, seatCount)

	// Fan out: every goroutine races for the same seat with its own request id
	// and its own customer. results[i] is written only by goroutine i, so no
	// mutex is needed on the slice itself. The close(start) is the starting gun.
	results := make([]FlashSale, attackers)
	var wg sync.WaitGroup
	for j := 0; j < 3; j++ {
		start := make(chan struct{})
		for i := 10000 * j; i < 10000*(j+1); i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				var seatID int
				if rand.Float64() < 0.8 {
					seatID = rand.Intn(rangeLow)
				} else {
					seatID = (rand.Intn(rangeHigh)) + rangeLow
				}
				startingTime := time.Now()
				out, retries, err := book(context.Background(), pool, eventID, seats[seatID],
					fmt.Sprintf("customer-%d", i), fmt.Sprintf("req-%d", i))
				if err != nil {
					results[i] = FlashSale{Outcome: OutcomeSystemError, totalTime: time.Since(startingTime), Batch: j, retries: retries, seatIdx: seatID}
					return
				}
				results[i] = FlashSale{Outcome: out, totalTime: time.Since(startingTime), Batch: j, retries: retries, seatIdx: seatID}
			}(i)
		}
		close(start)
		time.Sleep(time.Second * 6)
	}

	wg.Wait()
	batchMaxTime := make([]time.Duration, 3)
	batchp99Value := make([][]time.Duration, 3)
	totalRetries := 0
	attemptsWithRetries := 0
	counts := map[Outcome]int{}
	for _, o := range results {
		counts[o.Outcome]++
		totalRetries += o.retries
		if o.retries > 0 {
			attemptsWithRetries++
		}
		if batchMaxTime[o.Batch] < o.totalTime {
			batchMaxTime[o.Batch] = o.totalTime
		}

		batchp99Value[o.Batch] = append(batchp99Value[o.Batch], o.totalTime)

	}

	t.Logf("=== %d seats === outcomes: %v", seatCount, counts)
	t.Logf("Total Time for each batch: %v", batchMaxTime)
	t.Logf("retries: %d total across %d/%d attempts that retried at least once",
		totalRetries, attemptsWithRetries, attackers)

	for i, n := range batchp99Value {
		slices.Sort(n)
		p99 := n[int(float64(len(n))*0.99)]
		t.Logf("the p99 value for the batch %d is %v", i, p99)
	}

	for i, o := range results {
		if o.Outcome == OutcomeBooked || o.Outcome == OutcomeSeatTaken {
			out2, _, err := book(context.Background(), pool, eventID, seats[o.seatIdx], fmt.Sprintf("customer-%d", i), fmt.Sprintf("req-%d", i))
			if err != nil {
				t.Errorf("retry of req-%d errored: %v", i, err)
				continue
			}

			if !(o.Outcome == OutcomeBooked && out2 == OutcomeAlreadyMine) && !(o.Outcome == OutcomeSeatTaken && out2 == OutcomeSeatTaken) {
				t.Errorf("req-%d: original outcome %v, retry returned %v (seat idx %d)", i, o.Outcome, out2, o.seatIdx)
			}
		}
	}

	// Check 1 — exactly one winner (Guarantee 1: no over/undersell).
	if counts[OutcomeBooked] > seatCount || counts[OutcomeBooked] < rangeLow {
		t.Errorf("expected bookings between %d to %d, got %d", rangeLow, seatCount, counts[OutcomeBooked])
	}
	// Check 2 — everyone else lost cleanly, nobody crashed.
	if counts[OutcomeSystemError] != 0 {
		t.Errorf("expected 0 system errors, got %d", counts[OutcomeSystemError])
	}
	// Check 3a — reconciliation: the DB's row count must equal clients told "booked".
	var rows int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM bookings").Scan(&rows); err != nil {
		t.Fatalf("count bookings: %v", err)
	}
	if rows != counts[OutcomeBooked] {
		t.Errorf("DB has %d rows but %d clients were told booked", rows, counts[OutcomeBooked])
	}
	// Check 3b — no seat may appear twice. Totals alone can't catch a double-booked
	// seat masked by an unsold one, so ask the DB directly.
	var dupSeats int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM (SELECT seat_id FROM bookings GROUP BY seat_id HAVING count(*) > 1) dup").
		Scan(&dupSeats); err != nil {
		t.Fatalf("count duplicate seats: %v", err)
	}
	if dupSeats > 0 {
		t.Errorf("OVERSELL: %d seat(s) were booked more than once", dupSeats)
	}
}

var seatTestArr = []int{10000, 1000, 100, 10}

// Mechanism 3 — compare-and-swap.
func TestBookSeatCAS_NoDoubleBooking(t *testing.T) {
	for _, n := range seatTestArr {
		hammerOneSeat(t, BookSeatCAS, n)
	}
}

// Mechanism 2 — the unique constraint.
func TestBookSeat_NoDoubleBooking(t *testing.T) {
	for _, n := range seatTestArr {
		hammerOneSeat(t, BookSeat, n)
	}
}

// Mechanism 1 — the pessimistic row lock (SELECT ... FOR UPDATE).
func TestBookSeatRowLock_NoDoubleBooking(t *testing.T) {
	for _, n := range seatTestArr {
		hammerOneSeat(t, BookSeatRowLock, n)
	}
}

func TestBookSeatCAS_ConcurrentDuplicates(t *testing.T) {

	type PairMatching struct {
		Outcome   Outcome
		startTime time.Time
		endTime   time.Time
	}

	seatCount := 100

	const pairs = 1000

	pool, err := pgxpool.New(context.Background(), dsn())
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	eventID, seats := setupArena(t, pool, seatCount)

	var wg sync.WaitGroup
	var results [pairs][2]PairMatching
	start := make(chan struct{})
	for p := range pairs {
		seatID := seats[rand.Intn(seatCount)]
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(p int, i int) {
				defer wg.Done()
				<-start
				startTime := time.Now()
				out, _, _ := BookSeatCAS(context.Background(), pool, eventID, seatID, fmt.Sprintf("customer-%d", p), fmt.Sprintf("req-%d", p))
				endTime := time.Now()

				results[p][i] = PairMatching{Outcome: out, startTime: startTime, endTime: endTime}
			}(p, i)
		}
	}
	close(start)
	wg.Wait()

	dupCount := 0
	takenCount := 0
	overlapCount := 0

	for p, r := range results {
		if r[0].startTime.Before(r[1].endTime) && r[1].startTime.Before(r[0].endTime) {
			overlapCount++
		}
		switch {
		case (r[0].Outcome == OutcomeBooked && r[1].Outcome == OutcomeAlreadyMine) || (r[1].Outcome == OutcomeBooked && r[0].Outcome == OutcomeAlreadyMine):
			dupCount++

		case r[0].Outcome == OutcomeSeatTaken && r[1].Outcome == OutcomeSeatTaken:
			takenCount++

		default:
			t.Errorf("Got error in the pair %v, the out come values are %v and %v which is a illegal pair outcomes", p, r[0], r[1])
		}
	}

	t.Logf("The duplicate cases that ran concurrent are %v", dupCount)

	if overlapCount < (pairs / 2) {
		t.Errorf("only %d of %d pairs overlapped — the test cannot prove concurrency", overlapCount, pairs)
	} else {
		t.Logf("dup req that we genuinely fighting for each other in request lookup are %v", overlapCount)
	}
	t.Logf("The seat taken cases that ran concurrent are %v", takenCount)

	var bookings int
	err = pool.QueryRow(context.Background(), "SELECT count(*) FROM bookings").Scan(&bookings)
	if err != nil {
		t.Errorf("Got error from db when counting the bookings, %v", err)
	}

	if bookings != dupCount {
		t.Errorf("The pair booking count and the db actual booked rows differ,%v from code showing booked and %v showing bookings from table", dupCount, bookings)
	} else {
		t.Logf("Both match exactly at the bookings value %v", bookings)
	}

}
