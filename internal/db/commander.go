package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	commanderpolicy "parallel-intellect/internal/commandersession"
	"parallel-intellect/internal/digest"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/id"
	signalpolicy "parallel-intellect/internal/signals"
)

type CommanderLaunchContext struct {
	Mission          domain.Mission        `json:"mission"`
	ProjectName      string                `json:"project_name"`
	ProjectPath      string                `json:"project_path"`
	Tasks            []domain.Task         `json:"tasks"`
	Signals          []signalpolicy.Signal `json:"signals"`
	Events           []domain.Event        `json:"recent_events"`
	OperatorMessages []OperatorMessage     `json:"recent_operator_messages"`
	Digest           *digest.Artifact      `json:"mission_digest,omitempty"`
}

// OperatorMessage is durable operator direction for a mission. It deliberately
// does not depend on a particular commander session because replacement
// commanders need the same context as their predecessors.
type OperatorMessage struct {
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

const commanderPromptHistoryLimit = 12

func (s *Store) CommanderLaunchContext(ctx context.Context, missionID domain.MissionID) (CommanderLaunchContext, error) {
	mission, err := s.Mission(ctx, missionID)
	if err != nil {
		return CommanderLaunchContext{}, err
	}
	result := CommanderLaunchContext{Mission: mission}
	if err := s.db.QueryRowContext(ctx, "SELECT name, path FROM projects WHERE id = ?", mission.ProjectID).
		Scan(&result.ProjectName, &result.ProjectPath); err != nil {
		return CommanderLaunchContext{}, mapNotFound("load commander project", err)
	}
	result.Tasks, err = s.Tasks(ctx, missionID)
	if err != nil {
		return CommanderLaunchContext{}, err
	}
	result.Signals, err = s.Signals(ctx, ListSignalsFilter{MissionID: missionID})
	if err != nil {
		return CommanderLaunchContext{}, err
	}
	events, err := s.RecentMissionEvents(ctx, missionID, 50)
	if err != nil {
		return CommanderLaunchContext{}, err
	}
	result.Events = events
	result.OperatorMessages, err = s.RecentCommanderOperatorMessages(ctx, missionID, commanderPromptHistoryLimit)
	if err != nil {
		return CommanderLaunchContext{}, err
	}
	artifact, err := s.LatestMissionDigest(ctx, missionID)
	if err == nil {
		result.Digest = &artifact
	} else if !errors.Is(err, ErrNotFound) {
		return CommanderLaunchContext{}, err
	}
	return result, nil
}

// RecentCommanderOperatorMessages returns the bounded chronological tail of
// operator direction addressed to any commander session for a mission.
func (s *Store) RecentCommanderOperatorMessages(ctx context.Context, missionID domain.MissionID, limit int) ([]OperatorMessage, error) {
	if missionID == "" || limit < 1 {
		return nil, errors.New("mission and positive message limit are required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, body_json, created_at FROM (
		SELECT kind, body_json, created_at, id FROM messages
		WHERE mission_id = ? AND sender_kind = 'operator' AND recipient_kind = 'commander'
		ORDER BY created_at DESC, id DESC LIMIT ?
	) ORDER BY created_at, id`, missionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent commander operator messages: %w", err)
	}
	defer rows.Close()
	items := make([]OperatorMessage, 0, limit)
	for rows.Next() {
		var item OperatorMessage
		var body, created string
		if err := rows.Scan(&item.Kind, &body, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &struct {
			Message *string `json:"message"`
		}{Message: &item.Message}); err != nil {
			return nil, fmt.Errorf("decode commander operator message: %w", err)
		}
		if item.Message == "" {
			return nil, errors.New("commander operator message has no text")
		}
		item.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type RecordCommanderSessionInput struct {
	ProjectID domain.ProjectID        `json:"project_id,omitempty"`
	MissionID domain.MissionID        `json:"mission_id"`
	Session   domain.CommanderSession `json:"session"`
	Actor     string                  `json:"actor"`
}

func (s *Store) RecordCommanderSession(ctx context.Context, commandID domain.CommandID, in RecordCommanderSessionInput) (domain.CommanderSession, error) {
	if (in.MissionID == "" && in.ProjectID == "") || strings.TrimSpace(in.Actor) == "" || in.Session.ID == "" ||
		in.Session.Runtime == "" || in.Session.HerdrSessionName == "" || in.Session.HerdrWorkspaceID == "" ||
		in.Session.HerdrTabID == "" || in.Session.HerdrPaneID == "" || in.Session.HerdrAgentName == "" ||
		in.Session.AgentSessionID == "" {
		return domain.CommanderSession{}, errors.New("complete commander session identity is required")
	}
	return runCommand(ctx, s, commandID, "commander.session.record", in, func(tx *sql.Tx) (domain.CommanderSession, error) {
		projectID := in.ProjectID
		var mission domain.Mission
		if in.MissionID != "" {
			var err error
			mission, err = scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", in.MissionID))
			if err != nil {
				return domain.CommanderSession{}, err
			}
			if mission.CommanderSessionID != "" {
				return domain.CommanderSession{}, errors.New("mission already has a commander session")
			}
			if projectID != "" && projectID != mission.ProjectID {
				return domain.CommanderSession{}, errors.New("commander project does not own mission")
			}
			projectID = mission.ProjectID
		} else {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE id = ?", projectID).Scan(&exists); err != nil {
				return domain.CommanderSession{}, fmt.Errorf("load commander project: %w", err)
			}
			if exists != 1 {
				return domain.CommanderSession{}, ErrNotFound
			}
		}
		var existing int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM commander_sessions WHERE project_id = ? AND state NOT IN ('stopped', 'failed')", projectID).Scan(&existing); err != nil {
			return domain.CommanderSession{}, fmt.Errorf("inspect project commander: %w", err)
		}
		if existing != 0 {
			return domain.CommanderSession{}, errors.New("project already has a commander session")
		}
		var sequence int64
		if in.MissionID != "" {
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM events WHERE mission_id = ?", in.MissionID).Scan(&sequence); err != nil {
				return domain.CommanderSession{}, fmt.Errorf("load commander event cursor: %w", err)
			}
		}
		now := time.Now().UTC()
		session := in.Session
		session.MissionID, session.ProjectID = mission.ID, projectID
		session.State, session.Version = domain.CommanderSessionRunning, 1
		session.Budget = normalizeCommanderBudget(session.Budget)
		session.TurnCount = 1
		session.LastEventSequence = sequence
		session.CreatedAt, session.UpdatedAt = now, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO commander_sessions(
			id, project_id, mission_id, runtime, state, version, herdr_session_name,
			herdr_workspace_id, herdr_tab_id, herdr_pane_id, herdr_agent_name,
			agent_session_id, model, pi_extension_path, last_event_sequence, created_at, updated_at,
			max_turns, max_duration_ns, turn_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, session.ProjectID, nullableString(string(session.MissionID)), session.Runtime, session.State, session.Version,
			session.HerdrSessionName, session.HerdrWorkspaceID, session.HerdrTabID, session.HerdrPaneID,
			session.HerdrAgentName, session.AgentSessionID, nullableString(session.Model),
			nullableString(session.PiExtensionPath), session.LastEventSequence, formatTime(now), formatTime(now),
			session.Budget.MaxTurns, int64(session.Budget.MaxDuration), session.TurnCount); err != nil {
			return domain.CommanderSession{}, fmt.Errorf("insert commander session: %w", err)
		}
		if in.MissionID != "" {
			result, err := tx.ExecContext(ctx, `UPDATE missions SET commander_session_id = ?, version = version + 1
				WHERE id = ? AND commander_session_id IS NULL AND version = ?`, session.ID, mission.ID, mission.Version)
			if err != nil {
				return domain.CommanderSession{}, fmt.Errorf("bind commander mission: %w", err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 1 {
				return domain.CommanderSession{}, errors.New("stale commander mission binding")
			}
		}
		var missionID *domain.MissionID
		if session.MissionID != "" {
			missionID = &session.MissionID
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: missionID, Actor: in.Actor,
			Type: "commander.started", CommandID: &commandID, Payload: map[string]any{
				"commander_session_id": session.ID, "project_id": session.ProjectID, "runtime": session.Runtime,
				"herdr_session_name": session.HerdrSessionName, "herdr_workspace_id": session.HerdrWorkspaceID,
				"herdr_tab_id": session.HerdrTabID, "herdr_pane_id": session.HerdrPaneID,
			}}); err != nil {
			return domain.CommanderSession{}, err
		}
		return session, nil
	})
}

// ProjectCommanderSession returns the one persistent commander placement for
// a project, whether it is in conversational intake or bound to a mission.
func (s *Store) ProjectCommanderSession(ctx context.Context, projectID domain.ProjectID) (domain.CommanderSession, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM commander_sessions WHERE project_id = ? AND state NOT IN ('stopped', 'failed')", projectID).Scan(&count); err != nil {
		return domain.CommanderSession{}, fmt.Errorf("count project commanders: %w", err)
	}
	if count > 1 {
		return domain.CommanderSession{}, errors.New("project has multiple commander sessions")
	}
	session, err := scanCommanderSession(s.db.QueryRowContext(ctx, commanderSessionSelect+" WHERE project_id = ? AND state NOT IN ('stopped', 'failed')", projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CommanderSession{}, ErrNotFound
	}
	return session, err
}

func (s *Store) CommanderSession(ctx context.Context, missionID domain.MissionID) (domain.CommanderSession, error) {
	session, err := scanCommanderSession(s.db.QueryRowContext(ctx, commanderSessionSelect+" WHERE mission_id = ? AND state NOT IN ('stopped', 'failed')", missionID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CommanderSession{}, ErrNotFound
	}
	return session, err
}

func (s *Store) CommanderSessions(ctx context.Context) ([]domain.CommanderSession, error) {
	rows, err := s.db.QueryContext(ctx, commanderSessionSelect+" WHERE mission_id IS NOT NULL AND state NOT IN ('stopped', 'failed') ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.CommanderSession, 0)
	for rows.Next() {
		session, err := scanCommanderSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, session)
	}
	return result, rows.Err()
}

const commanderSessionSelect = `SELECT id, project_id, mission_id, runtime, state, version,
	herdr_session_name, herdr_workspace_id, herdr_tab_id, herdr_pane_id, herdr_agent_name,
	agent_session_id, model, pi_extension_path, last_event_sequence, created_at, updated_at,
	last_observed_at, stopped_at, failure_reason, max_turns, max_duration_ns, turn_count FROM commander_sessions`

func scanCommanderSession(row rowScanner) (domain.CommanderSession, error) {
	var session domain.CommanderSession
	var project, mission, herdrSession, workspace, tab, pane, agentName, agentSession sql.NullString
	var model, extension, created, updated, observed, stopped, reason sql.NullString
	var maxDuration int64
	if err := row.Scan(&session.ID, &project, &mission, &session.Runtime, &session.State, &session.Version,
		&herdrSession, &workspace, &tab, &pane, &agentName, &agentSession, &model, &extension,
		&session.LastEventSequence, &created, &updated, &observed, &stopped, &reason,
		&session.Budget.MaxTurns, &maxDuration, &session.TurnCount); err != nil {
		return domain.CommanderSession{}, err
	}
	session.ProjectID, session.MissionID = domain.ProjectID(project.String), domain.MissionID(mission.String)
	session.HerdrSessionName, session.HerdrWorkspaceID = herdrSession.String, workspace.String
	session.HerdrTabID, session.HerdrPaneID = tab.String, pane.String
	session.HerdrAgentName, session.AgentSessionID = agentName.String, agentSession.String
	session.Model, session.PiExtensionPath, session.FailureReason = model.String, extension.String, reason.String
	session.Budget.MaxDuration = time.Duration(maxDuration)
	var err error
	session.CreatedAt, err = parseTime(created.String)
	if err == nil {
		session.UpdatedAt, err = parseTime(updated.String)
	}
	for _, item := range []struct {
		source sql.NullString
		target **time.Time
	}{{observed, &session.LastObservedAt}, {stopped, &session.StoppedAt}} {
		if err == nil && item.source.Valid {
			var value time.Time
			value, err = parseTime(item.source.String)
			*item.target = &value
		}
	}
	return session, err
}

func normalizeCommanderBudget(value domain.CommanderBudget) domain.CommanderBudget {
	if value.MaxTurns == 0 {
		value.MaxTurns = 30
	}
	if value.MaxDuration == 0 {
		value.MaxDuration = 45 * time.Minute
	}
	return value
}

type CommanderSessionPlacement struct {
	HerdrWorkspaceID string `json:"herdr_workspace_id"`
	HerdrTabID       string `json:"herdr_tab_id"`
	HerdrPaneID      string `json:"herdr_pane_id"`
}

type ObserveCommanderSessionInput struct {
	SessionID       domain.SessionID             `json:"session_id"`
	ProjectID       domain.ProjectID             `json:"project_id,omitempty"`
	MissionID       domain.MissionID             `json:"mission_id"`
	ExpectedState   domain.CommanderSessionState `json:"expected_state"`
	ExpectedVersion int64                        `json:"expected_version"`
	ObservedState   domain.CommanderSessionState `json:"observed_state"`
	FailureReason   string                       `json:"failure_reason,omitempty"`
	Placement       *CommanderSessionPlacement   `json:"placement,omitempty"`
	Actor           string                       `json:"actor"`
}

func (s *Store) ObserveCommanderSession(ctx context.Context, commandID domain.CommandID, in ObserveCommanderSessionInput) (domain.CommanderSession, error) {
	if in.SessionID == "" || (in.ProjectID == "" && in.MissionID == "") || in.ExpectedVersion < 1 || strings.TrimSpace(in.Actor) == "" {
		return domain.CommanderSession{}, errors.New("session, project or mission, version, and actor are required")
	}
	if in.ExpectedState != in.ObservedState {
		if err := commanderpolicy.ValidateTransition(in.ExpectedState, in.ObservedState); err != nil {
			return domain.CommanderSession{}, err
		}
	}
	if in.Placement != nil && (in.Placement.HerdrWorkspaceID == "" || in.Placement.HerdrTabID == "" || in.Placement.HerdrPaneID == "") {
		return domain.CommanderSession{}, errors.New("complete commander replacement placement is required")
	}
	return runCommand(ctx, s, commandID, "commander.session.observe", in, func(tx *sql.Tx) (domain.CommanderSession, error) {
		current, err := getCommanderSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if (in.ProjectID != "" && current.ProjectID != in.ProjectID) ||
			(in.MissionID != "" && current.MissionID != in.MissionID) ||
			current.State != in.ExpectedState || current.Version != in.ExpectedVersion {
			return domain.CommanderSession{}, errors.New("stale commander-session observation")
		}
		workspace, tab, pane := current.HerdrWorkspaceID, current.HerdrTabID, current.HerdrPaneID
		if in.Placement != nil {
			if in.Placement.HerdrWorkspaceID != workspace || in.Placement.HerdrTabID == tab || in.Placement.HerdrPaneID == pane {
				return domain.CommanderSession{}, errors.New("commander replacement must stay in its workspace with distinct tab and pane")
			}
			tab, pane = in.Placement.HerdrTabID, in.Placement.HerdrPaneID
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET state = ?, version = version + 1,
			updated_at = ?, last_observed_at = ?, herdr_workspace_id = ?, herdr_tab_id = ?, herdr_pane_id = ?,
			failure_reason = ?, stopped_at = CASE WHEN ? = 'stopped' THEN ? ELSE stopped_at END
			WHERE id = ? AND project_id = ? AND state = ? AND version = ?`, in.ObservedState, formatTime(now),
			formatTime(now), workspace, tab, pane, nullableString(in.FailureReason), in.ObservedState, formatTime(now),
			in.SessionID, current.ProjectID, in.ExpectedState, in.ExpectedVersion)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.CommanderSession{}, errors.New("stale commander-session observation")
		}
		updated, err := getCommanderSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if in.ExpectedState != in.ObservedState || in.Placement != nil {
			var missionID *domain.MissionID
			if current.MissionID != "" {
				missionID = &current.MissionID
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: missionID, Actor: in.Actor,
				Type: "commander.session." + string(in.ObservedState), CommandID: &commandID,
				Payload: map[string]any{"commander_session_id": in.SessionID, "from": in.ExpectedState,
					"to": in.ObservedState, "herdr_pane_id": updated.HerdrPaneID, "reason": in.FailureReason}}); err != nil {
				return domain.CommanderSession{}, err
			}
		}
		return updated, nil
	})
}

// RetireCommanderSession terminally preserves a dead placement and releases
// its project/mission slot for a replacement commander. It is deliberately a
// distinct command from observation: a replacement must never make the dead
// record appear live again.
func (s *Store) RetireCommanderSession(ctx context.Context, commandID domain.CommandID, sessionID domain.SessionID, expectedState domain.CommanderSessionState, expectedVersion int64, actor, reason string) (domain.CommanderSession, error) {
	if sessionID == "" || expectedVersion < 1 || strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return domain.CommanderSession{}, errors.New("session, version, actor, and retirement reason are required")
	}
	if err := commanderpolicy.ValidateTransition(expectedState, domain.CommanderSessionStopped); err != nil {
		return domain.CommanderSession{}, err
	}
	in := struct {
		SessionID       domain.SessionID             `json:"session_id"`
		ExpectedState   domain.CommanderSessionState `json:"expected_state"`
		ExpectedVersion int64                        `json:"expected_version"`
		Actor           string                       `json:"actor"`
		Reason          string                       `json:"reason"`
	}{sessionID, expectedState, expectedVersion, actor, reason}
	return runCommand(ctx, s, commandID, "commander.session.retire", in, func(tx *sql.Tx) (domain.CommanderSession, error) {
		current, err := getCommanderSessionTx(ctx, tx, sessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if current.State != expectedState || current.Version != expectedVersion {
			return domain.CommanderSession{}, errors.New("stale commander-session retirement")
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET state = 'stopped', version = version + 1,
			updated_at = ?, last_observed_at = ?, stopped_at = ?, failure_reason = ?
			WHERE id = ? AND state = ? AND version = ?`, formatTime(now), formatTime(now), formatTime(now),
			reason, sessionID, expectedState, expectedVersion)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.CommanderSession{}, errors.New("stale commander-session retirement")
		}
		if current.MissionID != "" {
			result, err = tx.ExecContext(ctx, `UPDATE missions SET commander_session_id = NULL, version = version + 1
				WHERE id = ? AND commander_session_id = ?`, current.MissionID, current.ID)
			if err != nil {
				return domain.CommanderSession{}, fmt.Errorf("release retired commander mission binding: %w", err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 1 {
				return domain.CommanderSession{}, errors.New("stale retired commander mission binding")
			}
		}
		updated, err := getCommanderSessionTx(ctx, tx, sessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		var missionID *domain.MissionID
		if current.MissionID != "" {
			missionID = &current.MissionID
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: missionID, Actor: actor, Type: "commander.session.retired", CommandID: &commandID,
			Payload: map[string]any{"commander_session_id": current.ID, "from": current.State, "to": domain.CommanderSessionStopped, "reason": reason}}); err != nil {
			return domain.CommanderSession{}, err
		}
		return updated, nil
	})
}

func getCommanderSessionTx(ctx context.Context, tx *sql.Tx, sessionID domain.SessionID) (domain.CommanderSession, error) {
	return scanCommanderSession(tx.QueryRowContext(ctx, commanderSessionSelect+" WHERE id = ?", sessionID))
}

type RecordCommanderWakeInput struct {
	SessionID       domain.SessionID `json:"session_id"`
	MissionID       domain.MissionID `json:"mission_id"`
	ExpectedVersion int64            `json:"expected_version"`
	ObservedThrough int64            `json:"observed_through"`
	Delivered       []domain.Event   `json:"delivered"`
	Actor           string           `json:"actor"`
}

func (s *Store) RecordCommanderWake(ctx context.Context, commandID domain.CommandID, in RecordCommanderWakeInput) (domain.CommanderSession, error) {
	if in.SessionID == "" || in.MissionID == "" || in.ExpectedVersion < 1 || in.ObservedThrough < 0 || strings.TrimSpace(in.Actor) == "" {
		return domain.CommanderSession{}, errors.New("session, mission, version, cursor, and actor are required")
	}
	return runCommand(ctx, s, commandID, "commander.wake.record", in, func(tx *sql.Tx) (domain.CommanderSession, error) {
		current, err := getCommanderSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if current.MissionID != in.MissionID || current.Version != in.ExpectedVersion || in.ObservedThrough < current.LastEventSequence {
			return domain.CommanderSession{}, errors.New("stale commander wake cursor")
		}
		now := time.Now().UTC()
		for _, event := range in.Delivered {
			rawID, err := id.New("msg")
			if err != nil {
				return domain.CommanderSession{}, err
			}
			body, err := json.Marshal(event)
			if err != nil {
				return domain.CommanderSession{}, err
			}
			senderKind := "control_plane"
			var senderID any
			if event.TaskID != nil {
				senderKind, senderID = "worker", *event.TaskID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, mission_id, task_id, sender_kind,
				sender_id, recipient_kind, recipient_id, kind, body_json, event_sequence, created_at, delivered_at)
				VALUES (?, ?, ?, ?, ?, 'commander', ?, 'event_wake', ?, ?, ?, ?)`, rawID,
				in.MissionID, taskIDValue(event.TaskID), senderKind, senderID, in.SessionID, body,
				event.Sequence, formatTime(now), formatTime(now)); err != nil {
				return domain.CommanderSession{}, fmt.Errorf("record commander event message: %w", err)
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET last_event_sequence = ?,
			version = version + 1, updated_at = ? WHERE id = ? AND mission_id = ? AND version = ?`,
			in.ObservedThrough, formatTime(now), in.SessionID, in.MissionID, in.ExpectedVersion)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.CommanderSession{}, errors.New("stale commander wake cursor")
		}
		updated, err := getCommanderSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if len(in.Delivered) > 0 {
			if err := appendEvent(ctx, tx, eventInput{MissionID: &in.MissionID, Actor: in.Actor,
				Type: "commander.woken", CommandID: &commandID, Payload: map[string]any{
					"commander_session_id": in.SessionID, "event_count": len(in.Delivered),
					"through_sequence": in.ObservedThrough}}); err != nil {
				return domain.CommanderSession{}, err
			}
		}
		return updated, nil
	})
}

type RecordCommanderMessageInput struct {
	SessionID domain.SessionID `json:"session_id"`
	MissionID domain.MissionID `json:"mission_id"`
	Kind      string           `json:"kind"`
	Message   string           `json:"message"`
	Actor     string           `json:"actor"`
}

// RecordCommanderMessage makes operator-to-commander communication part of
// the family-scoped durable message/event model before it is delivered to
// Herdr, so session loss cannot lose operator intent.
func (s *Store) RecordCommanderMessage(ctx context.Context, commandID domain.CommandID, in RecordCommanderMessageInput) (domain.CommanderSession, error) {
	if in.SessionID == "" || in.MissionID == "" || strings.TrimSpace(in.Message) == "" || strings.TrimSpace(in.Actor) == "" {
		return domain.CommanderSession{}, errors.New("session, mission, message, and actor are required")
	}
	switch in.Kind {
	case "prompt", "steer", "follow_up":
	default:
		return domain.CommanderSession{}, fmt.Errorf("unknown commander message kind %q", in.Kind)
	}
	return runCommand(ctx, s, commandID, "commander.message.record", in, func(tx *sql.Tx) (domain.CommanderSession, error) {
		session, err := getCommanderSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if session.MissionID != in.MissionID {
			return domain.CommanderSession{}, errors.New("commander message mission mismatch")
		}
		rawID, err := id.New("msg")
		if err != nil {
			return domain.CommanderSession{}, err
		}
		body, err := json.Marshal(map[string]string{"message": in.Message})
		if err != nil {
			return domain.CommanderSession{}, err
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, mission_id, sender_kind,
			recipient_kind, recipient_id, kind, body_json, created_at, delivered_at)
			VALUES (?, ?, 'operator', 'commander', ?, ?, ?, ?, ?)`, rawID, in.MissionID,
			in.SessionID, in.Kind, body, formatTime(now), formatTime(now)); err != nil {
			return domain.CommanderSession{}, fmt.Errorf("record operator commander message: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &in.MissionID, Actor: in.Actor,
			Type: "commander." + in.Kind, CommandID: &commandID, Payload: map[string]any{
				"message_id": rawID, "commander_session_id": in.SessionID,
				"message": in.Message,
			}}); err != nil {
			return domain.CommanderSession{}, err
		}
		return session, nil
	})
}
