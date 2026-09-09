DB_URL := postgres://holdfast:holdfast_dev@localhost:5432/holdfast

# Which migration to apply. Override per run:
#   make migrate MIGRATION=migrations/003_idempotency.sql
MIGRATION ?= migrations/003_idempotency.sql

.PHONY: db-up db-down migrate run test

db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

migrate:
	docker compose exec -T db psql -v ON_ERROR_STOP=1 -U holdfast -d holdfast < $(MIGRATION)

run:
	DATABASE_URL=$(DB_URL) go run ./cmd/api

test:
	go test -race ./...
