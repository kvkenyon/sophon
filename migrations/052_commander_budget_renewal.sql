-- Execution-duration budget windows restart when an operator renews a commander.
ALTER TABLE commander_sessions ADD COLUMN budget_started_at TEXT;
UPDATE commander_sessions SET budget_started_at = created_at WHERE budget_started_at IS NULL;
