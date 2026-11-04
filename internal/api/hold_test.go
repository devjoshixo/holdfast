package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://holdfast:holdfast_dev@localhost:5432/holdfast?pool_max_conns=90"
}

func setupTestArena(t *testing.T, pool *pgxpool.Pool) (eventID int64, seatID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE bookings, seats, events, request_lookup RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO events (name, total_seats) VALUES ('api-test', 1) RETURNING id").Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO seats (event_id, label) VALUES ($1, 'seat-1') RETURNING id", eventID).Scan(&seatID); err != nil {
		t.Fatalf("insert seat: %v", err)
	}
	return eventID, seatID
}

func TestHandleHoldSeat_LifecycleAndRetry(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatID := setupTestArena(t, pool)

	server := NewServer(pool)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()
	holdURL := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/hold", ts.URL, eventID, seatID)

	// 1. Initial Hold -> 201 Created
	bodyA, _ := json.Marshal(HoldRequest{Customer: "cust-A"})
	req1, _ := http.NewRequest(http.MethodPost, holdURL, bytes.NewReader(bodyA))
	req1.Header.Set("Idempotency-Key", "req-A-1")
	req1.Header.Set("Content-Type", "application/json")

	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("first hold request failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp1.StatusCode)
	}

	var resData1 HoldResponse
	if err := json.NewDecoder(resp1.Body).Decode(&resData1); err != nil {
		t.Fatalf("decode 201 body: %v", err)
	}
	if resData1.Status != "hold_granted" {
		t.Errorf("expected status 'hold_granted', got %q", resData1.Status)
	}
	if resData1.HeldUntil.Before(time.Now()) {
		t.Errorf("expected HeldUntil in the future, got %v", resData1.HeldUntil)
	}

	// 2. Idempotent Retry -> 200 OK
	req2, _ := http.NewRequest(http.MethodPost, holdURL, bytes.NewReader(bodyA))
	req2.Header.Set("Idempotency-Key", "req-A-1")
	req2.Header.Set("Content-Type", "application/json")

	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("retry request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on idempotent retry, got %d", resp2.StatusCode)
	}

	var resData2 HoldResponse
	if err := json.NewDecoder(resp2.Body).Decode(&resData2); err != nil {
		t.Fatalf("decode 200 body: %v", err)
	}
	if resData2.Status != "hold_active" {
		t.Errorf("expected status 'hold_active', got %q", resData2.Status)
	}

	// 3. Competing Customer B -> 409 Conflict
	bodyB, _ := json.Marshal(HoldRequest{Customer: "cust-B"})
	req3, _ := http.NewRequest(http.MethodPost, holdURL, bytes.NewReader(bodyB))
	req3.Header.Set("Idempotency-Key", "req-B-1")
	req3.Header.Set("Content-Type", "application/json")

	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("customer B request failed: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for competing customer, got %d", resp3.StatusCode)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(resp3.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode 409 error body: %v", err)
	}
	if errResp.Error.Code != "SEAT_TAKEN" {
		t.Errorf("expected error code 'SEAT_TAKEN', got %q", errResp.Error.Code)
	}
}

func TestHandleHold_ConfirmAndReleaseFlow(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatID := setupTestArena(t, pool)

	server := NewServer(pool)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()
	holdURL := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/hold", ts.URL, eventID, seatID)
	confirmURL := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/confirm", ts.URL, eventID, seatID)
	releaseURL := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/release", ts.URL, eventID, seatID)

	// Step 1: Customer A holds seat
	bodyA, _ := json.Marshal(HoldRequest{Customer: "cust-A"})
	req1, _ := http.NewRequest(http.MethodPost, holdURL, bytes.NewReader(bodyA))
	req1.Header.Set("Idempotency-Key", "key-hold-1")
	resp1, err := client.Do(req1)
	if err != nil || resp1.StatusCode != http.StatusCreated {
		t.Fatalf("hold step failed: %v, status: %d", err, resp1.StatusCode)
	}
	resp1.Body.Close()

	// Step 2: Customer A confirms seat -> 200 OK
	confirmBody, _ := json.Marshal(ConfirmRequest{Customer: "cust-A"})
	reqConfirm, _ := http.NewRequest(http.MethodPost, confirmURL, bytes.NewReader(confirmBody))
	reqConfirm.Header.Set("Idempotency-Key", "key-confirm-1")
	respConfirm, err := client.Do(reqConfirm)
	if err != nil {
		t.Fatalf("confirm request failed: %v", err)
	}
	defer respConfirm.Body.Close()

	if respConfirm.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for confirm, got %d", respConfirm.StatusCode)
	}

	var confirmRes ConfirmResponse
	_ = json.NewDecoder(respConfirm.Body).Decode(&confirmRes)
	if confirmRes.Status != "booked" {
		t.Errorf("expected confirm status 'booked', got %q", confirmRes.Status)
	}

	// Step 3: Verify Release on new seat
	_, seatID2 := setupTestArena(t, pool)
	holdURL2 := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/hold", ts.URL, eventID, seatID2)
	releaseURL2 := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/release", ts.URL, eventID, seatID2)

	// Hold seat 2
	reqHold2, _ := http.NewRequest(http.MethodPost, holdURL2, bytes.NewReader(bodyA))
	reqHold2.Header.Set("Idempotency-Key", "key-hold-2")
	rHold2, _ := client.Do(reqHold2)
	rHold2.Body.Close()

	// Release seat 2 voluntarily -> 200 OK
	relBody, _ := json.Marshal(ReleaseRequest{Customer: "cust-A"})
	reqRel, _ := http.NewRequest(http.MethodPost, releaseURL2, bytes.NewReader(relBody))
	reqRel.Header.Set("Idempotency-Key", "key-release-1")
	respRel, err := client.Do(reqRel)
	if err != nil || respRel.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for release, got %d (err: %v)", respRel.StatusCode, err)
	}
	respRel.Body.Close()

	// Seat 2 is now free, Customer B can hold it -> 201 Created
	bodyB, _ := json.Marshal(HoldRequest{Customer: "cust-B"})
	reqHoldB, _ := http.NewRequest(http.MethodPost, holdURL2, bytes.NewReader(bodyB))
	reqHoldB.Header.Set("Idempotency-Key", "key-hold-b-2")
	respHoldB, err := client.Do(reqHoldB)
	if err != nil || respHoldB.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created after release, got %d (err: %v)", respHoldB.StatusCode, err)
	}
	respHoldB.Body.Close()
	_ = releaseURL
}

func TestHandleHoldSeat_ValidationRejections(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	eventID, seatID := setupTestArena(t, pool)

	server := NewServer(pool)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	// 1. Missing Idempotency-Key header -> 400 Bad Request
	holdURL := fmt.Sprintf("%s/api/v1/events/%d/seats/%d/hold", ts.URL, eventID, seatID)
	body, _ := json.Marshal(HoldRequest{Customer: "cust-A"})
	reqMissingHeader, _ := http.NewRequest(http.MethodPost, holdURL, bytes.NewReader(body))

	resp1, err := client.Do(reqMissingHeader)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing idempotency key, got %d", resp1.StatusCode)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(resp1.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errResp.Error.Code != "MISSING_IDEMPOTENCY_KEY" {
		t.Errorf("expected 'MISSING_IDEMPOTENCY_KEY', got %q", errResp.Error.Code)
	}

	// 2. Invalid seat ID path value (e.g. non-numeric) -> 400 Bad Request
	badURL := fmt.Sprintf("%s/api/v1/events/%d/seats/not-a-number/hold", ts.URL, eventID)
	reqBadPath, _ := http.NewRequest(http.MethodPost, badURL, bytes.NewReader(body))
	reqBadPath.Header.Set("Idempotency-Key", "valid-key-123")

	resp2, err := client.Do(reqBadPath)
	if err != nil {
		t.Fatalf("bad path request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid seat ID path, got %d", resp2.StatusCode)
	}
}
