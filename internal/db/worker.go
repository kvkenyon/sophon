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
		in.Session.HerdrWorkspaceID == "" || in.Session.HerdrTabID == "" || in.Session.HerdrPaneID == "" {
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
		session.CreatedAt, session.UpdatedAt = now, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_sessions(
			id, task_id, attempt, runtime, state, herdr_session_name, herdr_workspace_id,
			herdr_tab_id, herdr_pane_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, session.ID, session.TaskID, session.Attempt,
			session.Runtime, session.State, session.HerdrSessionName, session.HerdrWorkspaceID,
			session.HerdrTabID, session.HerdrPaneID, formatTime(now), formatTime(now)); err != nil {
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
	var session domain.WorkerSession
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, task_id, attempt, runtime, state, herdr_session_name,
		herdr_workspace_id, herdr_tab_id, herdr_pane_id, created_at, updated_at
		FROM worker_sessions WHERE task_id = ? AND attempt = ?`, taskID, attempt).Scan(
		&session.ID, &session.TaskID, &session.Attempt, &session.Runtime, &session.State,
		&session.HerdrSessionName, &session.HerdrWorkspaceID, &session.HerdrTabID,
		&session.HerdrPaneID, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerSession{}, ErrNotFound
	}
	if err != nil {
		return domain.WorkerSession{}, fmt.Errorf("load worker session: %w", err)
	}
	session.CreatedAt, err = parseTime(created)
	if err == nil {
		session.UpdatedAt, err = parseTime(updated)
	}
	if err != nil {
		return domain.WorkerSession{}, fmt.Errorf("parse worker session timestamp: %w", err)
	}
	return session, nil
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
