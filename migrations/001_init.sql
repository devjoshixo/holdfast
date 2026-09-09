CREATE TABLE events (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    total_seats INT  NOT NULL CHECK (total_seats > 0)
);

CREATE TABLE seats (
    id       BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES events (id),
    label    TEXT   NOT NULL,
    UNIQUE (event_id, label)
);

CREATE TABLE bookings (
    id BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES events (id),
    seat_id BIGINT NOT NULL REFERENCES seats (id),
    customer TEXT NOT NULL,
    request_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(event_id,seat_id)
)
