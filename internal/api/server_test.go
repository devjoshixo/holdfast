package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServer_StaticFileAndRoutePrecedence(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	server := NewServer(pool)
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	// 1. GET /healthz should return 200 {"status":"ok"} (API takes precedence)
	respHealth, err := client.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer respHealth.Body.Close()
	if respHealth.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /healthz, got %d", respHealth.StatusCode)
	}
	contentType := respHealth.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected application/json for /healthz, got %s", contentType)
	}

	// 2. GET / should return index.html (200 OK, text/html)
	respRoot, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer respRoot.Body.Close()
	if respRoot.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /, got %d", respRoot.StatusCode)
	}
	rootContentType := respRoot.Header.Get("Content-Type")
	if !strings.Contains(rootContentType, "text/html") {
		t.Errorf("expected text/html for /, got %s", rootContentType)
	}
}
