package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrencyLimiter_ShedsLoadUnderPeak(t *testing.T) {
	// Limiter with capacity 2
	limiter := NewConcurrencyLimiter(2)

	blocker := make(chan struct{})
	var inFlight atomic.Int32

	handler := limiter.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		<-blocker // hold the slot
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	var statuses [4]int

	// Fire 4 concurrent requests
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/hold", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			statuses[idx] = rec.Code
		}(i)
	}

	// Wait briefly for first 2 requests to acquire slots and block
	time.Sleep(50 * time.Millisecond)

	// Release the blocked requests
	close(blocker)
	wg.Wait()

	var okCount, rateLimitCount int
	for _, code := range statuses {
		if code == http.StatusOK {
			okCount++
		} else if code == http.StatusTooManyRequests {
			rateLimitCount++
		}
	}

	if okCount != 2 {
		t.Fatalf("expected 2 successful requests, got %d", okCount)
	}
	if rateLimitCount != 2 {
		t.Fatalf("expected 2 rate limited (429) requests, got %d", rateLimitCount)
	}
}
