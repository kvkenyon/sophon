CREATE TABLE artifacts (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL,
    attempt     INTEGER NOT NULL CHECK (attempt > 0),
    kind        TEXT NOT NULL,
    media_type  TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    content     BLOB NOT NULL,
    created_at  TEXT NOT NULL,
    FOREIGN KEY (task_id, attempt) REFERENCES task_attempts(task_id, attempt)
);

CREATE INDEX artifacts_task_attempt_idx ON artifacts(task_id, attempt, created_at);

CREATE TABLE validation_runs (
    id                   TEXT PRIMARY KEY,
    task_id              TEXT NOT NULL REFERENCES tasks(id),
    attempt              INTEGER NOT NULL CHECK (attempt > 0),
    head_sha             TEXT NOT NULL,
    workspace_hash       TEXT NOT NULL,
    validator            TEXT NOT NULL,
    validator_version    TEXT NOT NULL,
    config_hash          TEXT NOT NULL,
    command_hash         TEXT NOT NULL,
    environment_hash     TEXT NOT NULL,
    status               TEXT NOT NULL CHECK (status IN ('passed', 'failed')),
    artifact_id          TEXT NOT NULL UNIQUE REFERENCES artifacts(id),
    created_at           TEXT NOT NULL,
    FOREIGN KEY (task_id, attempt) REFERENCES task_attempts(task_id, attempt),
    UNIQUE (
        task_id,
        head_sha,
        workspace_hash,
        validator,
        validator_version,
        config_hash,
        command_hash,
        environment_hash
    )
);

CREATE INDEX validation_runs_task_created_idx ON validation_runs(task_id, created_at);
