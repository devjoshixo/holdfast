package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/retr0/holdfast/internal/api"
	"github.com/retr0/holdfast/internal/booking"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://holdfast:holdfast_dev@localhost:5432/holdfast"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()

	pool, err := pgxpool.New(initCtx, dsn)
	if err != nil {
		log.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(initCtx); err != nil {
		log.Fatalf("ping database: %v", err)
	}

	// Seed demo event and 40 seats if events table is empty
	if err := seedDemoDataIfEmpty(initCtx, pool); err != nil {
		log.Printf("holdfast: warning - seed demo data failed: %v", err)
	}

	// Root context for background workers
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// Launch background reaper (runs every 10 seconds, sweeps batch of 100)
	reaperInterval := 10 * time.Second
	reaperBatchSize := 100
	reaperDone := booking.StartReaper(appCtx, pool, reaperInterval, reaperBatchSize)
	log.Printf("holdfast: background reaper started (interval=%v, batch=%d)", reaperInterval, reaperBatchSize)

	server := api.NewServer(pool)
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           server,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan

		log.Println("holdfast: shutting down gracefully (SIGINT/SIGTERM received)...")

		// 1. Stop background workers
		appCancel()
		<-reaperDone
		log.Println("holdfast: background reaper stopped cleanly")

		// 2. Stop HTTP server
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("holdfast: http shutdown error: %v", err)
		}
		close(shutdownDone)
	}()

	log.Printf("holdfast: listening on :%s", port)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("holdfast: listen error: %v", err)
	}

	<-shutdownDone
	log.Println("holdfast: stopped cleanly")
}

// seedDemoDataIfEmpty seeds a demo event and 40 free seats (A1..D10) when events table has 0 rows.
func seedDemoDataIfEmpty(ctx context.Context, pool *pgxpool.Pool) error {
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		return fmt.Errorf("check events count: %w", err)
	}
	if count > 0 {
		return nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin seed tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var eventID int64
	err = tx.QueryRow(ctx,
		"INSERT INTO events (name, total_seats) VALUES ($1, $2) RETURNING id",
		"Neon Horizon Concert — Main Stage", 40,
	).Scan(&eventID)
	if err != nil {
		return fmt.Errorf("insert demo event: %w", err)
	}

	rows := []string{"A", "B", "C", "D"}
	for _, row := range rows {
		for num := 1; num <= 10; num++ {
			label := fmt.Sprintf("%s%d", row, num)
			_, err := tx.Exec(ctx,
				"INSERT INTO seats (event_id, label, status) VALUES ($1, $2, 'free')",
				eventID, label,
			)
			if err != nil {
				return fmt.Errorf("insert demo seat %s: %w", label, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit seed tx: %w", err)
	}

	log.Printf("holdfast: startup seed completed: event #%d ('Neon Horizon Concert — Main Stage') created with 40 seats (A1..D10)", eventID)
	return nil
}
