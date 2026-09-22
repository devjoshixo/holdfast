DB_URL := postgres://holdfast:holdfast_dev@localhost:5432/holdfast
PSQL   := docker compose exec -T db psql -v ON_ERROR_STOP=1 -U holdfast -d holdfast

.PHONY: db-up db-down db-reset migrate migrate-one run test

db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

# Drop the volume too. `db-down` keeps pgdata, so applied migrations survive and
# re-running one fails — this is the target for a genuinely clean slate.
db-reset:
	docker compose down -v

# Apply EVERY migration, in sorted order. Stops at the first failure.
# Migrations are not idempotent: run this against a fresh database.
migrate:
	@for f in $$(ls migrations/*.sql | sort); do \
		echo "  applying $$f"; \
		$(PSQL) < $$f || exit 1; \
	done

# Apply one file:  make migrate-one MIGRATION=migrations/004_holds.sql
MIGRATION ?= migrations/001_init.sql
migrate-one:
	$(PSQL) < $(MIGRATION)

run:
	DATABASE_URL=$(DB_URL) go run ./cmd/api

test:
	go test -race ./...
