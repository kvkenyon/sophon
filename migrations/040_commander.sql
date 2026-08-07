-- Milestone 7 extends the reserved commander_sessions projection in place.
-- Keeping the original table avoids rewriting the missions foreign key.
ALTER TABLE commander_sessions ADD COLUMN mission_id TEXT REFERENCES missions(id);
ALTER TABLE commander_sessions ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE commander_sessions ADD COLUMN herdr_session_name TEXT;
ALTER TABLE commander_sessions ADD COLUMN herdr_workspace_id TEXT;
ALTER TABLE commander_sessions ADD COLUMN herdr_tab_id TEXT;
ALTER TABLE commander_sessions ADD COLUMN herdr_pane_id TEXT;
ALTER TABLE commander_sessions ADD COLUMN herdr_agent_name TEXT;
ALTER TABLE commander_sessions ADD COLUMN agent_session_id TEXT;
ALTER TABLE commander_sessions ADD COLUMN model TEXT;
ALTER TABLE commander_sessions ADD COLUMN pi_extension_path TEXT;
ALTER TABLE commander_sessions ADD COLUMN last_observed_at TEXT;
ALTER TABLE commander_sessions ADD COLUMN last_event_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_event_sequence >= 0);
ALTER TABLE commander_sessions ADD COLUMN stopped_at TEXT;
ALTER TABLE commander_sessions ADD COLUMN failure_reason TEXT;

CREATE UNIQUE INDEX commander_sessions_mission_idx
    ON commander_sessions(mission_id) WHERE mission_id IS NOT NULL;
CREATE UNIQUE INDEX commander_sessions_herdr_pane_idx
    ON commander_sessions(herdr_session_name, herdr_pane_id)
    WHERE herdr_session_name IS NOT NULL AND herdr_pane_id IS NOT NULL;
CREATE INDEX commander_sessions_state_idx ON commander_sessions(state, updated_at);

CREATE TRIGGER commander_sessions_valid_state_insert
BEFORE INSERT ON commander_sessions
WHEN NEW.state NOT IN ('starting', 'running', 'idle', 'needs_attention', 'failed', 'stopping', 'stopped')
BEGIN
    SELECT RAISE(ABORT, 'invalid commander session state');
END;

CREATE TRIGGER commander_sessions_valid_state_update
BEFORE UPDATE OF state ON commander_sessions
WHEN NEW.state NOT IN ('starting', 'running', 'idle', 'needs_attention', 'failed', 'stopping', 'stopped')
BEGIN
    SELECT RAISE(ABORT, 'invalid commander session state');
END;

CREATE TRIGGER commander_sessions_valid_runtime_insert
BEFORE INSERT ON commander_sessions
WHEN NEW.runtime NOT IN ('pi', 'claude', 'codex')
BEGIN
    SELECT RAISE(ABORT, 'invalid commander runtime');
END;

CREATE TRIGGER commander_sessions_valid_runtime_update
BEFORE UPDATE OF runtime ON commander_sessions
WHEN NEW.runtime NOT IN ('pi', 'claude', 'codex')
BEGIN
    SELECT RAISE(ABORT, 'invalid commander runtime');
END;

-- Family-scoped message identity is explicit and mission bounded. Milestone 7
-- wires control-plane event messages to commanders; the same shape supports
-- the commander/worker and sibling-worker legs without a global broadcast.
CREATE TABLE messages (
    id                  TEXT PRIMARY KEY,
    mission_id          TEXT NOT NULL REFERENCES missions(id),
    task_id             TEXT REFERENCES tasks(id),
    sender_kind         TEXT NOT NULL CHECK (sender_kind IN ('operator', 'commander', 'worker', 'control_plane')),
    sender_id           TEXT,
    recipient_kind      TEXT NOT NULL CHECK (recipient_kind IN ('commander', 'worker')),
    recipient_id        TEXT NOT NULL,
    kind                TEXT NOT NULL,
    body_json           TEXT NOT NULL CHECK (json_valid(body_json)),
    event_sequence      INTEGER REFERENCES events(sequence),
    created_at          TEXT NOT NULL,
    delivered_at        TEXT,
    CHECK (length(trim(recipient_id)) > 0)
);

CREATE INDEX messages_recipient_idx
    ON messages(mission_id, recipient_kind, recipient_id, created_at);
CREATE UNIQUE INDEX messages_event_recipient_idx
    ON messages(event_sequence, recipient_kind, recipient_id)
    WHERE event_sequence IS NOT NULL;
