package api

import (
	"net/http"
)

// ConcurrencyLimiter is an admission-control middleware that bounds the number
// of concurrent in-flight requests to protect downstream dependencies (e.g. Postgres pool).
type ConcurrencyLimiter struct {
	sem chan struct{}
}

// NewConcurrencyLimiter initializes a limiter with maxConcurrent capacity.
func NewConcurrencyLimiter(maxConcurrent int) *ConcurrencyLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 50 // default sensible floor
	}
	return &ConcurrencyLimiter{
		sem: make(chan struct{}, maxConcurrent),
	}
}

// Limit wraps an http.Handler with non-blocking admission control.
// If the server is at max capacity, it sheds load immediately with 429 Too Many Requests.
func (c *ConcurrencyLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do not throttle health checks
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		select {
		case c.sem <- struct{}{}:
			// Acquired slot
			defer func() { <-c.sem }()
			next.ServeHTTP(w, r)
		default:
			// Capacity exhausted: shed load immediately to protect Postgres
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "OVERLOAD", "server at peak capacity; retry shortly")
		}
	})
}
