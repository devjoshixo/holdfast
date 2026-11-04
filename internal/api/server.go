package api

import (
	"net/http"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Server encapsulates HTTP routing and shared dependencies.
type Server struct {
	pool    *pgxpool.Pool
	mux     *http.ServeMux
	handler http.Handler
}

// NewServer registers all API routes and returns an initialized Server instance.
func NewServer(pool *pgxpool.Pool) *Server {
	maxConcurrent := 25
	if val := os.Getenv("MAX_CONCURRENT_REQUESTS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			maxConcurrent = n
		}
	}

	limiter := NewConcurrencyLimiter(maxConcurrent)

	s := &Server{
		pool: pool,
		mux:  http.NewServeMux(),
	}
	s.routes()
	s.handler = limiter.Limit(s.mux)
	return s
}

// ServeHTTP satisfies the standard http.Handler interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// Standard k8s liveness/readiness probe
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Seat inventory
	s.mux.HandleFunc("GET /api/v1/events/{event_id}/seats", s.handleListSeats)

	// Hold lifecycle
	s.mux.HandleFunc("POST /api/v1/events/{event_id}/seats/{seat_id}/hold", s.handleHoldSeat)
	s.mux.HandleFunc("POST /api/v1/events/{event_id}/seats/{seat_id}/confirm", s.handleConfirmSeat)
	s.mux.HandleFunc("POST /api/v1/events/{event_id}/seats/{seat_id}/release", s.handleReleaseHold)

	// Web UI static file server (serves web/ directory for GET /)
	// API routes take precedence due to Go 1.22+ ServeMux longest-match routing.
	webDir := os.Getenv("WEB_DIR")
	if webDir == "" {
		candidates := []string{"web", "holdfast/web", "../../web", "../web"}
		for _, c := range candidates {
			if info, err := os.Stat(c); err == nil && info.IsDir() {
				webDir = c
				break
			}
		}
		if webDir == "" {
			webDir = "web"
		}
	}
	s.mux.Handle("GET /", http.FileServer(http.Dir(webDir)))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
