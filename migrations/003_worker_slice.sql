ALTER TABLE tasks ADD COLUMN acceptance_criteria_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE task_attempts ADD COLUMN result_path TEXT;
ALTER TABLE task_attempts ADD COLUMN result_sha256 TEXT;
ALTER TABLE task_attempts ADD COLUMN result_json TEXT;

CREATE TABLE worker_sessions (
    id                  TEXT PRIMARY KEY,
    task_id             TEXT NOT NULL,
    attempt             INTEGER NOT NULL CHECK (attempt > 0),
    runtime             TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN ('starting', 'running', 'idle', 'lost')),
    herdr_session_name  TEXT NOT NULL,
    herdr_workspace_id  TEXT NOT NULL,
    herdr_tab_id        TEXT NOT NULL,
    herdr_pane_id       TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE (task_id, attempt),
    UNIQUE (herdr_session_name, herdr_pane_id),
    FOREIGN KEY (task_id, attempt) REFERENCES task_attempts(task_id, attempt)
);

CREATE INDEX worker_sessions_state_idx ON worker_sessions(state);
