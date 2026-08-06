CREATE TABLE signals (
    id                  TEXT PRIMARY KEY,
    mission_id          TEXT NOT NULL REFERENCES missions(id),
    task_id             TEXT REFERENCES tasks(id),
    kind                TEXT NOT NULL,
    question            TEXT NOT NULL,
    context             TEXT NOT NULL DEFAULT '',
    options_json        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(options_json)),
    recommendation      TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    answer              TEXT,
    version             INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at          TEXT NOT NULL,
    resolved_at         TEXT,
    CHECK (
        (status = 'open' AND answer IS NULL AND resolved_at IS NULL)
        OR
        (status = 'resolved' AND answer IS NOT NULL AND length(trim(answer)) > 0 AND resolved_at IS NOT NULL)
    )
);

CREATE INDEX signals_mission_status_idx ON signals(mission_id, status);
CREATE INDEX signals_task_status_idx ON signals(task_id, status);

CREATE TABLE task_signal_dependencies (
    task_id             TEXT NOT NULL REFERENCES tasks(id),
    signal_id           TEXT NOT NULL REFERENCES signals(id),
    created_at          TEXT NOT NULL,
    PRIMARY KEY (task_id, signal_id)
);

CREATE INDEX task_signal_dependencies_signal_idx ON task_signal_dependencies(signal_id);
