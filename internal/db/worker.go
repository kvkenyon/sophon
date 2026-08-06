package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"parallel-intellect/internal/domain"
	taskpolicy "parallel-intellect/internal/task"
	sessionpolicy "parallel-intellect/internal/workersession"
)

type TaskLaunchContext struct {
	Task                      domain.Task
	MissionTitle              string
	MissionObjective          string
	MissionAcceptanceCriteria []domain.Criterion
	ProjectName               string
	ProjectPath               string
}

func (s *Store) TaskLaunchContext(ctx context.Context, taskID domain.TaskID) (TaskLaunchContext, error) {
	t, err := s.Task(ctx, taskID)
	if err != nil {
		return TaskLaunchContext{}, err
	}
	var result TaskLaunchContext
	result.Task = t
	var missionCriteria []byte
	err = s.db.QueryRowContext(ctx, `SELECT m.title, m.objective, m.acceptance_criteria_json, p.name, p.path
		FROM missions m JOIN projects p ON p.id = m.project_id WHERE m.id = ?`, t.MissionID).
		Scan(&result.MissionTitle, &result.MissionObjective, &missionCriteria, &result.ProjectName, &result.ProjectPath)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskLaunchContext{}, ErrNotFound
	}
	if err != nil {
		return TaskLaunchContext{}, fmt.Errorf("load task launch context: %w", err)
	}
	if err := json.Unmarshal(missionCriteria, &result.MissionAcceptanceCriteria); err != nil {
		return TaskLaunchContext{}, fmt.Errorf("decode mission acceptance criteria: %w", err)
	}
	return result, nil
}

type RecordWorkerSessionInput struct {
	TaskID          domain.TaskID        `json:"task_id"`
	Attempt         int                  `json:"attempt"`
	ExpectedVersion int64                `json:"expected_version"`
	Session         domain.WorkerSession `json:"session"`
	Actor           string               `json:"actor"`
}

// RecordWorkerSession atomically binds the response-derived Herdr identity to
// the current attempt and advances starting -> running.
func (s *Store) RecordWorkerSession(ctx context.Context, commandID domain.CommandID, in RecordWorkerSessionInput) (domain.WorkerSession, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.Actor == "" ||
		in.Session.ID == "" || in.Session.Runtime == "" || in.Session.HerdrSessionName == "" ||
		in.Session.HerdrWorkspaceID == "" || in.Session.HerdrTabID == "" || in.Session.HerdrPaneID == "" ||
		in.Session.HerdrAgentName == "" || in.Session.AgentSessionID == "" {
		return domain.WorkerSession{}, errors.New("complete worker session identity is required")
	}
	return runCommand(ctx, s, commandID, "worker.session.record", in, func(tx *sql.Tx) (domain.WorkerSession, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return domain.WorkerSession{}, ErrStaleAttempt
		}
		if current.Version != in.ExpectedVersion || current.State != domain.TaskStarting {
			return domain.WorkerSession{}, &ConflictError{Current: current}
		}
		if err := taskpolicy.ValidateTransition(current, domain.TaskRunning); err != nil {
			return domain.WorkerSession{}, err
		}
		var activeLease int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM treehouse_leases
			WHERE task_id = ? AND attempt = ? AND state = 'active'`, in.TaskID, in.Attempt).Scan(&activeLease); err != nil {
			return domain.WorkerSession{}, fmt.Errorf("verify worker lease: %w", err)
		}
		if activeLease != 1 {
			return domain.WorkerSession{}, ErrLeaseConflict
		}
		now := time.Now().UTC()
		session := in.Session
		session.TaskID, session.Attempt = in.TaskID, in.Attempt
		session.State = domain.WorkerSessionRunning
		session.Version = 1
		session.CreatedAt, session.UpdatedAt = now, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_sessions(
			id, task_id, attempt, runtime, state, version, herdr_session_name, herdr_workspace_id,
			herdr_tab_id, herdr_pane_id, herdr_agent_name, agent_session_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, session.ID, session.TaskID, session.Attempt,
			session.Runtime, session.State, session.Version, session.HerdrSessionName, session.HerdrWorkspaceID,
			session.HerdrTabID, session.HerdrPaneID, session.HerdrAgentName, session.AgentSessionID,
			formatTime(now), formatTime(now)); err != nil {
			return domain.WorkerSession{}, fmt.Errorf("insert worker session: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_attempts SET worker_session_id = ?
			WHERE task_id = ? AND attempt = ? AND worker_session_id IS NULL`, session.ID, in.TaskID, in.Attempt); err != nil {
			return domain.WorkerSession{}, fmt.Errorf("bind worker session: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskRunning,
			formatTime(now), in.TaskID, domain.TaskStarting, in.ExpectedVersion, in.Attempt)
		if err != nil {
			return domain.WorkerSession{}, fmt.Errorf("start worker task: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return domain.WorkerSession{}, &ConflictError{Current: current}
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: "worker.started", CommandID: &commandID, Payload: map[string]any{
				"attempt": in.Attempt, "worker_session_id": session.ID, "runtime": session.Runtime,
				"herdr_session_name": session.HerdrSessionName, "herdr_workspace_id": session.HerdrWorkspaceID,
				"herdr_tab_id": session.HerdrTabID, "herdr_pane_id": session.HerdrPaneID,
			}}); err != nil {
			return domain.WorkerSession{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: "task.running", CommandID: &commandID, Payload: map[string]any{
				"from": domain.TaskStarting, "to": domain.TaskRunning, "version": in.ExpectedVersion + 1,
				"attempt": in.Attempt,
			}}); err != nil {
			return domain.WorkerSession{}, err
		}
		return session, nil
	})
}

func (s *Store) WorkerSession(ctx context.Context, taskID domain.TaskID, attempt int) (domain.WorkerSession, error) {
	session, err := scanWorkerSession(s.db.QueryRowContext(ctx, workerSessionSelect+
		" WHERE task_id = ? AND attempt = ?", taskID, attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerSession{}, ErrNotFound
	}
	if err != nil {
		return domain.WorkerSession{}, fmt.Errorf("load worker session: %w", err)
	}
	return session, nil
}

const workerSessionSelect = `SELECT id, task_id, attempt, runtime, state, version,
	herdr_session_name, herdr_workspace_id, herdr_tab_id, herdr_pane_id,
	herdr_agent_name, agent_session_id,
	created_at, updated_at, last_observed_at, idle_at, inactive_at,
	recovery_prompt_at, stopped_at, failure_reason FROM worker_sessions`

type workerRowScanner interface {
	Scan(...any) error
}

func scanWorkerSession(row workerRowScanner) (domain.WorkerSession, error) {
	var session domain.WorkerSession
	var created, updated string
	var agentSession, observed, idle, inactive, recovery, stopped, reason sql.NullString
	if err := row.Scan(&session.ID, &session.TaskID, &session.Attempt, &session.Runtime,
		&session.State, &session.Version, &session.HerdrSessionName, &session.HerdrWorkspaceID,
		&session.HerdrTabID, &session.HerdrPaneID, &session.HerdrAgentName, &agentSession,
		&created, &updated, &observed, &idle,
		&inactive, &recovery, &stopped, &reason); err != nil {
		return domain.WorkerSession{}, err
	}
	var err error
	if session.CreatedAt, err = parseTime(created); err != nil {
		return domain.WorkerSession{}, err
	}
	if session.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.WorkerSession{}, err
	}
	for _, item := range []struct {
		value  sql.NullString
		target **time.Time
	}{
		{observed, &session.LastObservedAt}, {idle, &session.IdleAt},
		{inactive, &session.InactiveAt}, {recovery, &session.RecoveryPromptAt},
		{stopped, &session.StoppedAt},
	} {
		value, target := item.value, item.target
		if !value.Valid {
			continue
		}
		parsed, parseErr := parseTime(value.String)
		if parseErr != nil {
			return domain.WorkerSession{}, parseErr
		}
		*target = &parsed
	}
	if reason.Valid {
		session.FailureReason = reason.String
	}
	if agentSession.Valid {
		session.AgentSessionID = agentSession.String
	}
	return session, nil
}

func getWorkerSessionTx(ctx context.Context, tx *sql.Tx, sessionID domain.SessionID) (domain.WorkerSession, error) {
	session, err := scanWorkerSession(tx.QueryRowContext(ctx, workerSessionSelect+" WHERE id = ?", sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerSession{}, ErrNotFound
	}
	return session, err
}

type TransitionWorkerSessionInput struct {
	SessionID       domain.SessionID          `json:"session_id"`
	TaskID          domain.TaskID             `json:"task_id"`
	Attempt         int                       `json:"attempt"`
	ExpectedState   domain.WorkerSessionState `json:"expected_state"`
	ExpectedVersion int64                     `json:"expected_version"`
	To              domain.WorkerSessionState `json:"to"`
	Actor           string                    `json:"actor"`
	FailureReason   string                    `json:"failure_reason,omitempty"`
	Placement       *WorkerSessionPlacement   `json:"placement,omitempty"`
}

// WorkerSessionPlacement changes only the external Herdr location of the
// same logical worker session. It is used when a restart leaves a dead husk
// and Codex is resumed in a replacement pane in the same workspace.
type WorkerSessionPlacement struct {
	HerdrWorkspaceID string `json:"herdr_workspace_id"`
	HerdrTabID       string `json:"herdr_tab_id"`
	HerdrPaneID      string `json:"herdr_pane_id"`
}

// TransitionWorkerSession updates only the worker-session projection. It
// deliberately cannot complete or otherwise advance the task projection.
func (s *Store) TransitionWorkerSession(ctx context.Context, commandID domain.CommandID, in TransitionWorkerSessionInput) (domain.WorkerSession, error) {
	if in.SessionID == "" || in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.Actor == "" {
		return domain.WorkerSession{}, errors.New("session, task, attempt, expected version, and actor are required")
	}
	if err := sessionpolicy.ValidateTransition(in.ExpectedState, in.To); err != nil {
		return domain.WorkerSession{}, err
	}
	if in.Placement != nil && (in.Placement.HerdrWorkspaceID == "" || in.Placement.HerdrTabID == "" || in.Placement.HerdrPaneID == "") {
		return domain.WorkerSession{}, errors.New("complete Herdr replacement placement is required")
	}
	return runCommand(ctx, s, commandID, "worker.session.transition", in, func(tx *sql.Tx) (domain.WorkerSession, error) {
		current, err := getWorkerSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		if current.TaskID != in.TaskID || current.Attempt != in.Attempt {
			return domain.WorkerSession{}, ErrStaleAttempt
		}
		if current.State != in.ExpectedState || current.Version != in.ExpectedVersion {
			return domain.WorkerSession{}, errors.New("stale worker-session transition")
		}
		workspaceID, tabID, paneID := current.HerdrWorkspaceID, current.HerdrTabID, current.HerdrPaneID
		if in.Placement != nil {
			if in.Placement.HerdrWorkspaceID != current.HerdrWorkspaceID {
				return domain.WorkerSession{}, errors.New("worker replacement must stay in its Herdr workspace")
			}
			if in.Placement.HerdrTabID == current.HerdrTabID || in.Placement.HerdrPaneID == current.HerdrPaneID {
				return domain.WorkerSession{}, errors.New("worker replacement must have distinct tab and pane identity")
			}
			tabID, paneID = in.Placement.HerdrTabID, in.Placement.HerdrPaneID
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE worker_sessions SET
			state = ?, version = version + 1, updated_at = ?, last_observed_at = ?,
			herdr_workspace_id = ?, herdr_tab_id = ?, herdr_pane_id = ?,
			idle_at = CASE WHEN ? = 'idle' THEN ? WHEN ? = 'running' THEN NULL ELSE idle_at END,
			inactive_at = CASE WHEN ? = 'inactive' THEN ? WHEN ? = 'running' THEN NULL ELSE inactive_at END,
			stopped_at = CASE WHEN ? = 'stopped' THEN ? ELSE stopped_at END,
			failure_reason = CASE WHEN ? IN ('failed', 'lost') THEN ? ELSE failure_reason END
			WHERE id = ? AND task_id = ? AND attempt = ? AND state = ? AND version = ?`,
			in.To, formatTime(now), formatTime(now), workspaceID, tabID, paneID,
			in.To, formatTime(now), in.To,
			in.To, formatTime(now), in.To, in.To, formatTime(now), in.To, nullableString(in.FailureReason),
			in.SessionID, in.TaskID, in.Attempt, in.ExpectedState, in.ExpectedVersion)
		if err != nil {
			return domain.WorkerSession{}, fmt.Errorf("transition worker session: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return domain.WorkerSession{}, errors.New("stale worker-session transition")
		}
		updated, err := getWorkerSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		task, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &task.MissionID, TaskID: &task.ID,
			Actor: in.Actor, Type: "worker.session." + string(in.To), CommandID: &commandID,
			Payload: map[string]any{"worker_session_id": in.SessionID, "from": in.ExpectedState,
				"to": in.To, "version": updated.Version, "attempt": in.Attempt,
				"herdr_workspace_id": updated.HerdrWorkspaceID, "herdr_tab_id": updated.HerdrTabID,
				"herdr_pane_id": updated.HerdrPaneID,
			}}); err != nil {
			return domain.WorkerSession{}, err
		}
		return updated, nil
	})
}

type ReserveRecoveryPromptInput struct {
	SessionID       domain.SessionID `json:"session_id"`
	TaskID          domain.TaskID    `json:"task_id"`
	Attempt         int              `json:"attempt"`
	ExpectedVersion int64            `json:"expected_version"`
	Actor           string           `json:"actor"`
}

// ReserveRecoveryPrompt durably enforces at-most-one recovery prompt before
// the external Herdr call is made.
func (s *Store) ReserveRecoveryPrompt(ctx context.Context, commandID domain.CommandID, in ReserveRecoveryPromptInput) (domain.WorkerSession, error) {
	if in.SessionID == "" || in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.Actor == "" {
		return domain.WorkerSession{}, errors.New("session, task, attempt, expected version, and actor are required")
	}
	return runCommand(ctx, s, commandID, "worker.recovery.reserve", in, func(tx *sql.Tx) (domain.WorkerSession, error) {
		current, err := getWorkerSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		if current.TaskID != in.TaskID || current.Attempt != in.Attempt {
			return domain.WorkerSession{}, ErrStaleAttempt
		}
		if (current.State != domain.WorkerSessionIdle && current.State != domain.WorkerSessionInactive) || current.Version != in.ExpectedVersion {
			return domain.WorkerSession{}, errors.New("stale worker recovery reservation")
		}
		if current.RecoveryPromptAt != nil {
			return domain.WorkerSession{}, ErrRecoveryPrompted
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE worker_sessions SET recovery_prompt_at = ?,
			version = version + 1, updated_at = ? WHERE id = ? AND state IN ('idle', 'inactive')
			AND version = ? AND recovery_prompt_at IS NULL`, formatTime(now), formatTime(now),
			in.SessionID, in.ExpectedVersion)
		if err != nil {
			return domain.WorkerSession{}, fmt.Errorf("reserve recovery prompt: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return domain.WorkerSession{}, ErrRecoveryPrompted
		}
		updated, err := getWorkerSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		task, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.WorkerSession{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &task.MissionID, TaskID: &task.ID,
			Actor: in.Actor, Type: "worker.recovery.reserved", CommandID: &commandID,
			Payload: map[string]any{"worker_session_id": in.SessionID, "attempt": in.Attempt,
				"recovery_prompt_at": updated.RecoveryPromptAt,
			}}); err != nil {
			return domain.WorkerSession{}, err
		}
		return updated, nil
	})
}

type ReconcileLostWorkerInput struct {
	SessionID       domain.SessionID          `json:"session_id"`
	TaskID          domain.TaskID             `json:"task_id"`
	Attempt         int                       `json:"attempt"`
	ExpectedState   domain.WorkerSessionState `json:"expected_state"`
	ExpectedVersion int64                     `json:"expected_version"`
	TaskVersion     int64                     `json:"task_version"`
	Reason          string                    `json:"reason"`
	Actor           string                    `json:"actor"`
}

// ReconcileLostWorker atomically records the missing runtime and escalates the
// task. It never acquires a replacement session or alters the active lease.
func (s *Store) ReconcileLostWorker(ctx context.Context, commandID domain.CommandID, in ReconcileLostWorkerInput) (domain.Task, error) {
	if err := sessionpolicy.ValidateTransition(in.ExpectedState, domain.WorkerSessionLost); err != nil {
		return domain.Task{}, err
	}
	return runCommand(ctx, s, commandID, "worker.session.lost", in, func(tx *sql.Tx) (domain.Task, error) {
		session, err := getWorkerSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.Task{}, err
		}
		task, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if session.TaskID != in.TaskID || session.Attempt != in.Attempt || task.CurrentAttempt != in.Attempt {
			return domain.Task{}, ErrStaleAttempt
		}
		if session.State != in.ExpectedState || session.Version != in.ExpectedVersion || task.Version != in.TaskVersion {
			return domain.Task{}, errors.New("stale lost-worker reconciliation")
		}
		if err := taskpolicy.ValidateTransition(task, domain.TaskNeedsAttention); err != nil {
			return domain.Task{}, err
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE worker_sessions SET state = 'lost', version = version + 1,
			updated_at = ?, last_observed_at = ?, failure_reason = ? WHERE id = ? AND state = ? AND version = ?`,
			formatTime(now), formatTime(now), in.Reason, in.SessionID, in.ExpectedState, in.ExpectedVersion); err != nil {
			return domain.Task{}, fmt.Errorf("mark worker session lost: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskNeedsAttention,
			formatTime(now), in.TaskID, task.State, in.TaskVersion, in.Attempt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("escalate lost worker task: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return domain.Task{}, errors.New("stale lost-worker reconciliation")
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &task.MissionID, TaskID: &task.ID,
			Actor: in.Actor, Type: "worker.session.lost", CommandID: &commandID,
			Payload: map[string]any{"worker_session_id": in.SessionID, "attempt": in.Attempt,
				"reason": in.Reason,
			}}); err != nil {
			return domain.Task{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &task.MissionID, TaskID: &task.ID,
			Actor: in.Actor, Type: "task.needs_attention", CommandID: &commandID,
			Payload: map[string]any{"from": task.State, "to": domain.TaskNeedsAttention,
				"version": updated.Version, "attempt": in.Attempt, "reason": "worker_lost",
			}}); err != nil {
			return domain.Task{}, err
		}
		return updated, nil
	})
}

type CompleteWorkerTaskInput struct {
	TaskID          domain.TaskID       `json:"task_id"`
	Attempt         int                 `json:"attempt"`
	ExpectedVersion int64               `json:"expected_version"`
	LeaseID         string              `json:"lease_id"`
	LeaseHolder     string              `json:"lease_holder"`
	HeadSHA         string              `json:"head_sha"`
	ResultPath      string              `json:"result_path"`
	ResultSHA256    string              `json:"result_sha256"`
	Result          domain.WorkerResult `json:"result"`
	Actor           string              `json:"actor"`
}

// CompleteWorkerTask records an already independently verified result and
// advances running -> collecting -> ready in one idempotent transaction.
func (s *Store) CompleteWorkerTask(ctx context.Context, commandID domain.CommandID, in CompleteWorkerTaskInput) (domain.Task, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.LeaseID == "" ||
		in.LeaseHolder == "" || in.HeadSHA == "" || in.ResultPath == "" || in.ResultSHA256 == "" || in.Actor == "" {
		return domain.Task{}, errors.New("complete verified worker outcome is required")
	}
	return runCommand(ctx, s, commandID, "worker.complete", in, func(tx *sql.Tx) (domain.Task, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return domain.Task{}, ErrStaleAttempt
		}
		if current.Version != in.ExpectedVersion || current.State != domain.TaskRunning {
			return domain.Task{}, &ConflictError{Current: current}
		}
		if err := taskpolicy.ValidateTransition(current, domain.TaskCollecting); err != nil {
			return domain.Task{}, err
		}
		var leaseCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM treehouse_leases WHERE task_id = ?
			AND attempt = ? AND lease_id = ? AND lease_holder = ? AND state = 'active'`, in.TaskID,
			in.Attempt, in.LeaseID, in.LeaseHolder).Scan(&leaseCount); err != nil {
			return domain.Task{}, fmt.Errorf("verify completion lease: %w", err)
		}
		if leaseCount != 1 {
			return domain.Task{}, ErrLeaseConflict
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskCollecting,
			formatTime(now), in.TaskID, domain.TaskRunning, in.ExpectedVersion, in.Attempt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("collect worker result: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return domain.Task{}, &ConflictError{Current: current}
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: "task.collecting", CommandID: &commandID, Payload: map[string]any{
				"from": domain.TaskRunning, "to": domain.TaskCollecting, "version": in.ExpectedVersion + 1,
				"attempt": in.Attempt, "head_sha": in.HeadSHA,
			}}); err != nil {
			return domain.Task{}, err
		}
		resultJSON, err := json.Marshal(in.Result)
		if err != nil {
			return domain.Task{}, fmt.Errorf("encode worker result: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_attempts SET head_sha = ?, result_path = ?,
			result_sha256 = ?, result_json = ?, completed_at = COALESCE(completed_at, ?)
			WHERE task_id = ? AND attempt = ?`, in.HeadSHA, in.ResultPath, in.ResultSHA256,
			resultJSON, formatTime(now), in.TaskID, in.Attempt); err != nil {
			return domain.Task{}, fmt.Errorf("record worker result: %w", err)
		}
		collecting := current
		collecting.State = domain.TaskCollecting
		collecting.Version++
		if err := taskpolicy.ValidateTransition(collecting, domain.TaskReady); err != nil {
			return domain.Task{}, err
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskReady,
			formatTime(now), in.TaskID, domain.TaskCollecting, collecting.Version, in.Attempt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("ready worker result: %w", err)
		}
		rows, err = result.RowsAffected()
		if err != nil || rows != 1 {
			return domain.Task{}, &ConflictError{Current: current}
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
			Actor: in.Actor, Type: "task.ready", CommandID: &commandID, Payload: map[string]any{
				"from": domain.TaskCollecting, "to": domain.TaskReady, "version": updated.Version,
				"attempt": in.Attempt, "head_sha": in.HeadSHA, "result_sha256": in.ResultSHA256,
			}}); err != nil {
			return domain.Task{}, err
		}
		return updated, nil
	})
}
