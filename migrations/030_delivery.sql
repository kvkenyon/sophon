CREATE TABLE deliveries (
    task_id       TEXT NOT NULL,
    attempt       INTEGER NOT NULL CHECK (attempt > 0),
    mode          TEXT NOT NULL CHECK (mode IN ('gate', 'pr', 'branch')),
    repository    TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL,
    head_sha      TEXT NOT NULL,
    pr_url        TEXT,
    pr_number     INTEGER CHECK (pr_number > 0),
    state         TEXT NOT NULL CHECK (state IN ('pending', 'blocked', 'delivered_branch', 'delivered')),
    gate_state    TEXT NOT NULL CHECK (gate_state IN ('not_required', 'pending', 'passed', 'failed')),
    gate_output   TEXT,
    command_id    TEXT NOT NULL REFERENCES commands(id),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    delivered_at TEXT,
    PRIMARY KEY (task_id, attempt),
    FOREIGN KEY (task_id, attempt) REFERENCES task_attempts(task_id, attempt),
    CHECK ((pr_url IS NULL) = (pr_number IS NULL))
);

CREATE INDEX deliveries_state_idx ON deliveries(state, updated_at);
