package booking

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ReapExpiredHolds sweeps through seats in 'held' status whose held_until has passed,
// atomically resetting them to 'free'. Uses FOR UPDATE SKIP LOCKED to ensure multiple
// workers or pods can sweep concurrently without blocking or deadlocking each other.
func ReapExpiredHolds(ctx context.Context, pool *pgxpool.Pool, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}

	query := `
		WITH expired AS (
			SELECT id 
			FROM seats
			WHERE status = 'held' AND held_until <= now()
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE seats s
		SET status = 'free',
		    held_by = NULL,
		    held_until = NULL
		FROM expired e
		WHERE s.id = e.id
	`

	tag, err := pool.Exec(ctx, query, batchSize)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}

// StartReaper launches a background worker that periodically calls ReapExpiredHolds.
// It stops cleanly when ctx is canceled, closing the returned done channel.
func StartReaper(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, batchSize int) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Sweep in a loop until backlog is cleared (< batchSize)
				for {
					count, err := ReapExpiredHolds(ctx, pool, batchSize)
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						log.Printf("reaper: error sweeping holds: %v", err)
						break
					}
					if count > 0 {
						log.Printf("reaper: freed %d expired seats", count)
					}
					// If we freed less than batchSize, backlog is drained for this tick
					if count < int64(batchSize) {
						break
					}
				}
			}
		}
	}()

	return done
}
