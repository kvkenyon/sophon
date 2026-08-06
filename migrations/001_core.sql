CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    path        TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL
);

CREATE TABLE commander_sessions (
    id          TEXT PRIMARY KEY,
    project_id  TEXT REFERENCES projects(id),
    runtime     TEXT NOT NULL,
    state       TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE missions (
    id                          TEXT PRIMARY KEY,
    project_id                  TEXT NOT NULL REFERENCES projects(id),
    commander_session_id        TEXT REFERENCES commander_sessions(id),
    title                       TEXT NOT NULL,
    objective                   TEXT NOT NULL,
    acceptance_criteria_json    TEXT NOT NULL DEFAULT '[]',
    state                       TEXT NOT NULL CHECK (state IN ('active', 'completing', 'completed', 'cancelled')),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    max_wall_clock_ns           INTEGER NOT NULL DEFAULT 0,
    max_concurrent_tasks        INTEGER NOT NULL DEFAULT 0,
    max_task_attempts           INTEGER NOT NULL DEFAULT 0,
    max_validation_runs         INTEGER NOT NULL DEFAULT 0,
    max_tokens                  INTEGER,
    max_cost                    TEXT,
    created_at                  TEXT NOT NULL,
    completed_at                TEXT
);

CREATE TABLE tasks (
    id                  TEXT PRIMARY KEY,
    mission_id          TEXT NOT NULL REFERENCES missions(id),
    parent_task_id      TEXT REFERENCES tasks(id),
    base_task_id        TEXT REFERENCES tasks(id),
    base_sha            TEXT,
    kind                TEXT NOT NULL CHECK (kind IN ('implementation', 'scout', 'review')),
    title               TEXT NOT NULL,
    objective           TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN (
                            'queued', 'provisioning', 'starting', 'running', 'blocked',
                            'collecting', 'ready', 'report_ready', 'validating',
                            'delivery_blocked', 'delivered', 'delivered_branch',
                            'needs_attention', 'cancelling', 'cancelled', 'failed'
                        )),
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    priority            INTEGER NOT NULL DEFAULT 0,
    worker_agent        TEXT,
    delivery_mode       TEXT NOT NULL CHECK (delivery_mode IN ('gate', 'pr', 'branch')),
    current_attempt     INTEGER NOT NULL DEFAULT 1 CHECK (current_attempt > 0),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT
);

CREATE INDEX tasks_mission_state_idx ON tasks(mission_id, state);

CREATE TABLE task_attempts (
    task_id                     TEXT NOT NULL REFERENCES tasks(id),
    attempt                     INTEGER NOT NULL CHECK (attempt > 0),
    base_sha                    TEXT,
    head_sha                    TEXT,
    branch                      TEXT,
    worktree_path               TEXT,
    treehouse_lease_id          TEXT,
    treehouse_lease_holder      TEXT,
    worker_session_id           TEXT,
    created_at                  TEXT NOT NULL,
    started_at                  TEXT,
    completed_at                TEXT,
    PRIMARY KEY (task_id, attempt)
);

CREATE TABLE task_dependencies (
    task_id             TEXT NOT NULL REFERENCES tasks(id),
    depends_on_task_id  TEXT NOT NULL REFERENCES tasks(id),
    created_at          TEXT NOT NULL,
    PRIMARY KEY (task_id, depends_on_task_id),
    CHECK (task_id <> depends_on_task_id)
);

CREATE TABLE events (
    sequence        INTEGER PRIMARY KEY AUTOINCREMENT,
    mission_id      TEXT REFERENCES missions(id),
    task_id         TEXT REFERENCES tasks(id),
    actor           TEXT NOT NULL,
    type            TEXT NOT NULL,
    command_id      TEXT REFERENCES commands(id),
    payload_json    TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL
);

CREATE INDEX events_mission_sequence_idx ON events(mission_id, sequence);
CREATE INDEX events_task_sequence_idx ON events(task_id, sequence);

CREATE TRIGGER events_no_update
BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TRIGGER events_no_delete
BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TABLE commands (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL,
    request_hash    TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('running', 'completed')),
    result_json     TEXT,
    created_at      TEXT NOT NULL,
    completed_at    TEXT
);
