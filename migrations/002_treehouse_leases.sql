CREATE TABLE treehouse_leases (
    lease_id       TEXT PRIMARY KEY,
    task_id        TEXT NOT NULL,
    attempt        INTEGER NOT NULL CHECK (attempt > 0),
    lease_holder   TEXT NOT NULL,
    worktree_path  TEXT NOT NULL,
    project        TEXT NOT NULL,
    branch         TEXT NOT NULL,
    base_sha       TEXT NOT NULL,
    state          TEXT NOT NULL CHECK (state IN ('active', 'released', 'fenced', 'missing')),
    acquired_at    TEXT NOT NULL,
    released_at    TEXT,
    UNIQUE (task_id, attempt),
    FOREIGN KEY (task_id, attempt) REFERENCES task_attempts(task_id, attempt)
);

CREATE INDEX treehouse_leases_state_idx ON treehouse_leases(state);
