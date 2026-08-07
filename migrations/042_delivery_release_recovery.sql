-- A durable release intent distinguishes a completed external return from an
-- unexpected lease mismatch during startup reconciliation.
ALTER TABLE deliveries ADD COLUMN release_command_id TEXT;
ALTER TABLE deliveries ADD COLUMN release_state TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN release_actor TEXT NOT NULL DEFAULT '';

CREATE INDEX deliveries_release_state_idx ON deliveries(release_state, updated_at);
