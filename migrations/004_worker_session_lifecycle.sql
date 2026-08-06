ALTER TABLE worker_sessions RENAME TO worker_sessions_m3;

CREATE TABLE worker_sessions (
    id                  TEXT PRIMARY KEY,
    task_id             TEXT NOT NULL,
    attempt             INTEGER NOT NULL CHECK (attempt > 0),
    runtime             TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN (
                            'starting', 'running', 'idle', 'inactive',
                            'lost', 'failed', 'stopping', 'stopped'
                        )),
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    herdr_session_name  TEXT NOT NULL,
    herdr_workspace_id  TEXT NOT NULL,
    herdr_tab_id        TEXT NOT NULL,
    herdr_pane_id       TEXT NOT NULL,
    herdr_agent_name    TEXT NOT NULL DEFAULT '',
    agent_session_id    TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    last_observed_at    TEXT,
    idle_at             TEXT,
    inactive_at         TEXT,
    recovery_prompt_at  TEXT,
    stopped_at          TEXT,
    failure_reason      TEXT,
    UNIQUE (task_id, attempt),
    UNIQUE (herdr_session_name, herdr_pane_id),
    FOREIGN KEY (task_id, attempt) REFERENCES task_attempts(task_id, attempt)
);

INSERT INTO worker_sessions(
    id, task_id, attempt, runtime, state, version, herdr_session_name,
    herdr_workspace_id, herdr_tab_id, herdr_pane_id, herdr_agent_name,
    agent_session_id, created_at, updated_at
)
SELECT id, task_id, attempt, runtime, state, 1, herdr_session_name,
       herdr_workspace_id, herdr_tab_id, herdr_pane_id, '', NULL,
       created_at, updated_at
FROM worker_sessions_m3;

DROP TABLE worker_sessions_m3;

CREATE INDEX worker_sessions_state_idx ON worker_sessions(state);
CREATE INDEX worker_sessions_recovery_idx
    ON worker_sessions(state, recovery_prompt_at, idle_at);
