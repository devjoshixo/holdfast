package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/retr0/holdfast/internal/api"
)

const loadBalancerURL = "http://localhost:8080"

func TestMultiInstance_ConcurrentHoldContention(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Fetch available seats through the load balancer
	resp, err := client.Get(fmt.Sprintf("%s/api/v1/events/1/seats", loadBalancerURL))
	if err != nil {
		t.Fatalf("failed to query seats via load balancer: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from load balancer, got %d", resp.StatusCode)
	}

	var seats []api.SeatView
	if err := json.NewDecoder(resp.Body).Decode(&seats); err != nil {
		t.Fatalf("failed to decode seats: %v", err)
	}

	// Find the first free seat to attack
	var targetSeat *api.SeatView
	for _, s := range seats {
		if s.Status == "free" {
			targetSeat = &s
			break
		}
	}

	if targetSeat == nil {
		t.Fatalf("no free seats available for test; re-seed the event")
	}

	t.Logf("Targeting seat %s (ID: %d) through Load Balancer", targetSeat.Label, targetSeat.ID)

	// 2. Launch 50 concurrent buyers across all replicas
	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	startBarrier := make(chan struct{})

	type result struct {
		statusCode int
		backend    string
		outcome    string
	}

	results := make([]result, concurrency)

	for i := 0; i < concurrency; i++ {
		idx := i
		buyerName := fmt.Sprintf("buyer-%02d", idx)
		idempotencyKey := fmt.Sprintf("req-m5-%d-%d", targetSeat.ID, idx)

		go func() {
			defer wg.Done()

			holdURL := fmt.Sprintf("%s/api/v1/events/1/seats/%d/hold", loadBalancerURL, targetSeat.ID)
			payload, _ := json.Marshal(api.HoldRequest{Customer: buyerName})

			req, _ := http.NewRequest(http.MethodPost, holdURL, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", idempotencyKey)

			// Hold all 50 goroutines at the barrier
			<-startBarrier

			res, err := client.Do(req)
			if err != nil {
				results[idx] = result{statusCode: 0, backend: "error"}
				return
			}
			defer res.Body.Close()

			bodyBytes, _ := io.ReadAll(res.Body)
			backend := res.Header.Get("X-Backend-Server")

			var holdRes api.HoldResponse
			_ = json.Unmarshal(bodyBytes, &holdRes)

			results[idx] = result{
				statusCode: res.StatusCode,
				backend:    backend,
				outcome:    holdRes.Status,
			}
		}()
	}

	// Blast off! All 50 hit Nginx simultaneously
	close(startBarrier)
	wg.Wait()

	// 3. Analyze results across all replicas
	successCount := 0
	conflictCount := 0
	backendHits := make(map[string]int)

	for _, r := range results {
		if r.statusCode == http.StatusCreated {
			successCount++
		} else if r.statusCode == http.StatusConflict {
			conflictCount++
		}
		if r.backend != "" {
			backendHits[r.backend]++
		}
	}

	t.Logf("=== Load Balancer Distribution ===")
	for backend, count := range backendHits {
		t.Logf("  Backend Replica %s handled %d requests", backend, count)
	}

	t.Logf("=== Contention Outcomes ===")
	t.Logf("  201 Created (Won the hold):   %d", successCount)
	t.Logf("  409 Conflict (Seat taken):    %d", conflictCount)

	// 4. Invariant Assertions
	if len(backendHits) < 2 {
		t.Errorf("expected traffic to hit multiple replicas, only hit: %v", backendHits)
	}

	if successCount != 1 {
		t.Fatalf("VIOLATION: expected exactly 1 winner, got %d", successCount)
	}

	if conflictCount != concurrency-1 {
		t.Fatalf("VIOLATION: expected %d conflicts, got %d", concurrency-1, conflictCount)
	}

	t.Logf("SUCCESS: Multi-instance correctness intact under 50-client load-balanced burst!")
}
