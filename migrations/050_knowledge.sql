-- Milestone 12: regenerable mission digests, governed knowledge, and the
-- counters/configuration needed to enforce autonomous execution budgets.
CREATE TABLE mission_digest_artifacts (
    id                       TEXT PRIMARY KEY,
    mission_id               TEXT NOT NULL REFERENCES missions(id),
    kind                     TEXT NOT NULL DEFAULT 'mission.digest' CHECK (kind = 'mission.digest'),
    media_type               TEXT NOT NULL DEFAULT 'text/markdown' CHECK (media_type = 'text/markdown'),
    sha256                   TEXT NOT NULL,
    content                  BLOB NOT NULL,
    based_on_event_sequence  INTEGER NOT NULL DEFAULT 0 CHECK (based_on_event_sequence >= 0),
    created_by               TEXT NOT NULL,
    created_at               TEXT NOT NULL
);

CREATE INDEX mission_digest_artifacts_latest_idx
    ON mission_digest_artifacts(mission_id, created_at DESC, id DESC);

CREATE TABLE knowledge (
    id                    TEXT PRIMARY KEY,
    project_id            TEXT NOT NULL REFERENCES projects(id),
    mission_id            TEXT REFERENCES missions(id),
    scope                 TEXT NOT NULL CHECK (scope IN ('immutable-policy', 'project', 'learned', 'mission')),
    kind                  TEXT NOT NULL,
    content               TEXT NOT NULL CHECK (length(trim(content)) > 0),
    created_by            TEXT NOT NULL,
    origin                TEXT NOT NULL CHECK (origin IN ('operator', 'commander', 'agent', 'system')),
    trigger_task_id       TEXT REFERENCES tasks(id),
    evidence_artifact_id  TEXT REFERENCES artifacts(id),
    confidence            REAL NOT NULL CHECK (confidence >= 0.0 AND confidence <= 1.0),
    status                TEXT NOT NULL CHECK (status IN ('candidate', 'active', 'rejected', 'superseded')),
    created_at            TEXT NOT NULL,
    superseded_by         TEXT REFERENCES knowledge(id),
    CHECK ((scope = 'mission' AND mission_id IS NOT NULL) OR scope <> 'mission'),
    CHECK ((status = 'superseded' AND superseded_by IS NOT NULL) OR
           (status <> 'superseded' AND superseded_by IS NULL)),
    CHECK (superseded_by IS NULL OR superseded_by <> id)
);

CREATE INDEX knowledge_project_scope_status_idx
    ON knowledge(project_id, scope, status, created_at, id);
CREATE INDEX knowledge_mission_status_idx
    ON knowledge(mission_id, status, created_at, id) WHERE mission_id IS NOT NULL;

-- Defense in depth for invariant 14. The Go policy rejects these writes
-- before SQL; these triggers ensure alternate SQL callers cannot bypass it.
CREATE TRIGGER knowledge_no_agent_critical_insert
BEFORE INSERT ON knowledge
WHEN NEW.origin = 'agent' AND (
    NEW.scope = 'immutable-policy' OR lower(replace(replace(NEW.kind, '_', '-'), ' ', '-')) IN (
        'task-state-machine', 'worktree-lease-policy', 'destructive-operation-policy',
        'credentials-policy', 'merge-policy', 'pr-delivery-policy', 'security-boundary',
        'completion-requirements', 'operator-authority', 'delivery-policy',
        'state-machine', 'lease-policy', 'destructive-policy', 'credentials',
        'merge-authority', 'security-rules', 'completion-requirement'
    )
)
BEGIN
    SELECT RAISE(ABORT, 'agent-originated critical-policy write refused');
END;

CREATE TRIGGER knowledge_no_agent_critical_update
BEFORE UPDATE ON knowledge
WHEN NEW.origin = 'agent' AND (
    OLD.scope = 'immutable-policy' OR NEW.scope = 'immutable-policy' OR lower(replace(replace(OLD.kind, '_', '-'), ' ', '-')) IN (
        'task-state-machine', 'worktree-lease-policy', 'destructive-operation-policy',
        'credentials-policy', 'merge-policy', 'pr-delivery-policy', 'security-boundary',
        'completion-requirements', 'operator-authority', 'delivery-policy',
        'state-machine', 'lease-policy', 'destructive-policy', 'credentials',
        'merge-authority', 'security-rules', 'completion-requirement'
    ) OR lower(replace(replace(NEW.kind, '_', '-'), ' ', '-')) IN (
        'task-state-machine', 'worktree-lease-policy', 'destructive-operation-policy',
        'credentials-policy', 'merge-policy', 'pr-delivery-policy', 'security-boundary',
        'completion-requirements', 'operator-authority', 'delivery-policy',
        'state-machine', 'lease-policy', 'destructive-policy', 'credentials',
        'merge-authority', 'security-rules', 'completion-requirement'
    )
)
BEGIN
    SELECT RAISE(ABORT, 'agent-originated critical-policy write refused');
END;

ALTER TABLE worker_sessions ADD COLUMN max_runtime_ns INTEGER NOT NULL DEFAULT 5400000000000 CHECK (max_runtime_ns >= 0);
ALTER TABLE worker_sessions ADD COLUMN max_restarts INTEGER NOT NULL DEFAULT 2 CHECK (max_restarts >= 0);
ALTER TABLE worker_sessions ADD COLUMN max_fix_rounds INTEGER NOT NULL DEFAULT 5 CHECK (max_fix_rounds >= 0);
ALTER TABLE worker_sessions ADD COLUMN restart_count INTEGER NOT NULL DEFAULT 0 CHECK (restart_count >= 0);
ALTER TABLE worker_sessions ADD COLUMN fix_round_count INTEGER NOT NULL DEFAULT 0 CHECK (fix_round_count >= 0);

ALTER TABLE commander_sessions ADD COLUMN max_turns INTEGER NOT NULL DEFAULT 30 CHECK (max_turns >= 0);
ALTER TABLE commander_sessions ADD COLUMN max_duration_ns INTEGER NOT NULL DEFAULT 2700000000000 CHECK (max_duration_ns >= 0);
ALTER TABLE commander_sessions ADD COLUMN turn_count INTEGER NOT NULL DEFAULT 0 CHECK (turn_count >= 0);
