CREATE TABLE request_lookup(
    request_id TEXT NOT NULL PRIMARY KEY,
    outcome TEXT NULL,
    seat_id BIGINT NOT NULL REFERENCES seats(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)
