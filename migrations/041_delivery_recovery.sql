-- Preserve the original delivery request inputs needed to replay the M10
-- idempotent external reconciliation path after a daemon restart.
ALTER TABLE deliveries ADD COLUMN request_base TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN request_actor TEXT NOT NULL DEFAULT 'operator';
