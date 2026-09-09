ALTER TABLE seats 
ADD COLUMN held_by TEXT NULL,
ADD COLUMN held_until TIMESTAMPTZ NULL,
ADD CONSTRAINT held_requires_holder CHECK (status <> 'held' OR (held_by IS NOT NULL AND held_until IS NOT NULL));