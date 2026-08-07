package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/id"
	taskpolicy "parallel-intellect/internal/task"
)

type CreateProjectInput struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Store) CreateProject(ctx context.Context, commandID domain.CommandID, in CreateProjectInput) (domain.ProjectID, error) {
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Path) == "" {
		return "", errors.New("project name and path are required")
	}
	return runCommand(ctx, s, commandID, "project.create", in, func(tx *sql.Tx) (domain.ProjectID, error) {
		rawID, err := id.New("prj")
		if err != nil {
			return "", err
		}
		projectID := domain.ProjectID(rawID)
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO projects(id, name, path, created_at) VALUES (?, ?, ?, ?)",
			projectID, in.Name, in.Path, formatTime(time.Now())); err != nil {
			return "", fmt.Errorf("insert project: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{
			Actor: "operator", Type: "project.created", CommandID: &commandID,
			Payload: map[string]any{"project_id": projectID, "name": in.Name},
		}); err != nil {
			return "", err
		}
		return projectID, nil
	})
}

func (s *Store) ProjectByPath(ctx context.Context, path string) (domain.ProjectID, error) {
	var projectID domain.ProjectID
	err := s.db.QueryRowContext(ctx, "SELECT id FROM projects WHERE path = ?", path).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load project by path: %w", err)
	}
	return projectID, nil
}

type CreateMissionInput struct {
	ProjectID          domain.ProjectID     `json:"project_id"`
	CommanderSessionID domain.SessionID     `json:"commander_session_id,omitempty"`
	OperatorMessage    string               `json:"operator_message,omitempty"`
	Title              string               `json:"title"`
	Objective          string               `json:"objective"`
	AcceptanceCriteria []domain.Criterion   `json:"acceptance_criteria,omitempty"`
	Budget             domain.MissionBudget `json:"budget"`
}

func (s *Store) CreateMission(ctx context.Context, commandID domain.CommandID, in CreateMissionInput) (domain.Mission, error) {
	if in.ProjectID == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Objective) == "" {
		return domain.Mission{}, errors.New("project, title, and objective are required")
	}
	return runCommand(ctx, s, commandID, "mission.create", in, func(tx *sql.Tx) (domain.Mission, error) {
		operatorMessage := strings.TrimSpace(in.OperatorMessage)
		commanderSessionID := in.CommanderSessionID
		if commanderSessionID == "" {
			err := tx.QueryRowContext(ctx, `SELECT id FROM commander_sessions
				WHERE project_id = ? AND mission_id IS NULL AND state NOT IN ('stopped', 'failed') ORDER BY created_at LIMIT 1`, in.ProjectID).
				Scan(&commanderSessionID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return domain.Mission{}, fmt.Errorf("load project intake commander: %w", err)
			}
		} else {
			var projectID domain.ProjectID
			var missionID sql.NullString
			if err := tx.QueryRowContext(ctx, "SELECT project_id, mission_id FROM commander_sessions WHERE id = ? AND state NOT IN ('stopped', 'failed')", commanderSessionID).
				Scan(&projectID, &missionID); err != nil {
				return domain.Mission{}, mapNotFound("load mission commander", err)
			}
			if projectID != in.ProjectID || missionID.Valid {
				return domain.Mission{}, errors.New("commander is not the project's intake session")
			}
		}
		rawID, err := id.New("msn")
		if err != nil {
			return domain.Mission{}, err
		}
		criteria, err := json.Marshal(in.AcceptanceCriteria)
		if err != nil {
			return domain.Mission{}, fmt.Errorf("encode acceptance criteria: %w", err)
		}
		now := time.Now().UTC()
		mission := domain.Mission{
			ID: domain.MissionID(rawID), ProjectID: in.ProjectID,
			CommanderSessionID: commanderSessionID, Title: in.Title, Objective: in.Objective,
			AcceptanceCriteria: in.AcceptanceCriteria, State: domain.MissionActive,
			Version: 1, Budget: in.Budget, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO missions(
			id, project_id, commander_session_id, title, objective, acceptance_criteria_json,
			state, version, max_wall_clock_ns, max_concurrent_tasks, max_task_attempts,
			max_validation_runs, max_tokens, max_cost, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			mission.ID, mission.ProjectID, nullableString(string(mission.CommanderSessionID)),
			mission.Title, mission.Objective, criteria, mission.State, mission.Version,
			int64(mission.Budget.MaxWallClock), mission.Budget.MaxConcurrentTasks,
			mission.Budget.MaxTaskAttempts, mission.Budget.MaxValidationRuns,
			mission.Budget.MaxTokens, mission.Budget.MaxCost, formatTime(now)); err != nil {
			return domain.Mission{}, fmt.Errorf("insert mission: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &mission.ID, Actor: "operator", Type: "mission.created", CommandID: &commandID,
			Payload: map[string]any{"state": mission.State, "version": mission.Version},
		}); err != nil {
			return domain.Mission{}, err
		}
		if commanderSessionID != "" {
			var sequence int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM events WHERE mission_id = ?", mission.ID).Scan(&sequence); err != nil {
				return domain.Mission{}, fmt.Errorf("load mission creation cursor: %w", err)
			}
			result, err := tx.ExecContext(ctx, `UPDATE commander_sessions
				SET mission_id = ?, last_event_sequence = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND project_id = ? AND mission_id IS NULL`, mission.ID, sequence,
				formatTime(now), commanderSessionID, mission.ProjectID)
			if err != nil {
				return domain.Mission{}, fmt.Errorf("bind intake commander to mission: %w", err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 1 {
				return domain.Mission{}, errors.New("stale intake commander binding")
			}
		}
		if operatorMessage != "" {
			if commanderSessionID == "" {
				return domain.Mission{}, errors.New("operator message requires an intake commander session")
			}
			messageID, err := id.New("msg")
			if err != nil {
				return domain.Mission{}, err
			}
			body, err := json.Marshal(map[string]string{"message": operatorMessage})
			if err != nil {
				return domain.Mission{}, fmt.Errorf("encode intake operator message: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, mission_id, sender_kind,
				recipient_kind, recipient_id, kind, body_json, created_at, delivered_at)
				VALUES (?, ?, 'operator', 'commander', ?, 'intake', ?, ?, ?)`, messageID, mission.ID,
				commanderSessionID, body, formatTime(now), formatTime(now)); err != nil {
				return domain.Mission{}, fmt.Errorf("record intake operator message: %w", err)
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: &mission.ID, Actor: "operator", Type: "commander.intake",
				CommandID: &commandID, Payload: map[string]any{"message_id": messageID,
					"commander_session_id": commanderSessionID, "message": operatorMessage}}); err != nil {
				return domain.Mission{}, err
			}
		}
		if _, err := regenerateMissionDigestTx(ctx, tx, mission.ID, "control-plane", "mission.created", &commandID); err != nil {
			return domain.Mission{}, err
		}
		return mission, nil
	})
}

type CreateTaskInput struct {
	MissionID          domain.MissionID    `json:"mission_id"`
	ParentTaskID       *domain.TaskID      `json:"parent_task_id,omitempty"`
	BaseTaskID         *domain.TaskID      `json:"base_task_id,omitempty"`
	BaseSHA            string              `json:"base_sha,omitempty"`
	Kind               domain.TaskKind     `json:"kind"`
	Title              string              `json:"title"`
	Objective          string              `json:"objective"`
	AcceptanceCriteria []domain.Criterion  `json:"acceptance_criteria,omitempty"`
	Priority           int                 `json:"priority"`
	WorkerAgent        string              `json:"worker_agent,omitempty"`
	DeliveryMode       domain.DeliveryMode `json:"delivery_mode"`
	Branch             string              `json:"branch,omitempty"`
}

func (s *Store) CreateTask(ctx context.Context, commandID domain.CommandID, in CreateTaskInput) (domain.Task, error) {
	if err := validateCreateTask(in); err != nil {
		return domain.Task{}, err
	}
	return runCommand(ctx, s, commandID, "task.create", in, func(tx *sql.Tx) (domain.Task, error) {
		var missionState domain.MissionState
		if err := tx.QueryRowContext(ctx, "SELECT state FROM missions WHERE id = ?", in.MissionID).Scan(&missionState); err != nil {
			return domain.Task{}, mapNotFound("load task mission", err)
		}
		if missionState != domain.MissionActive && missionState != domain.MissionCompleting {
			return domain.Task{}, fmt.Errorf("cannot create task for mission in state %q", missionState)
		}
		rawID, err := id.New("tsk")
		if err != nil {
			return domain.Task{}, err
		}
		now := time.Now().UTC()
		criteria, err := json.Marshal(in.AcceptanceCriteria)
		if err != nil {
			return domain.Task{}, fmt.Errorf("encode task acceptance criteria: %w", err)
		}
		t := domain.Task{
			ID: domain.TaskID(rawID), MissionID: in.MissionID, ParentTaskID: in.ParentTaskID,
			BaseTaskID: in.BaseTaskID, BaseSHA: in.BaseSHA, Kind: in.Kind, Title: in.Title,
			Objective: in.Objective, AcceptanceCriteria: in.AcceptanceCriteria,
			State: domain.TaskQueued, Version: 1, Priority: in.Priority,
			WorkerAgent: in.WorkerAgent, DeliveryMode: in.DeliveryMode, CurrentAttempt: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(
			id, mission_id, parent_task_id, base_task_id, base_sha, kind, title, objective, acceptance_criteria_json,
			state, version, priority, worker_agent, delivery_mode, current_attempt, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.MissionID, taskIDValue(t.ParentTaskID), taskIDValue(t.BaseTaskID), nullableString(t.BaseSHA),
			t.Kind, t.Title, t.Objective, criteria, t.State, t.Version, t.Priority, nullableString(t.WorkerAgent),
			t.DeliveryMode, t.CurrentAttempt, formatTime(now), formatTime(now)); err != nil {
			return domain.Task{}, fmt.Errorf("insert task: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_attempts(task_id, attempt, base_sha, branch, created_at) VALUES (?, 1, ?, ?, ?)`,
			t.ID, nullableString(in.BaseSHA), nullableString(in.Branch), formatTime(now)); err != nil {
			return domain.Task{}, fmt.Errorf("insert first attempt: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &t.MissionID, TaskID: &t.ID, Actor: "commander", Type: "task.created", CommandID: &commandID,
			Payload: map[string]any{"state": t.State, "version": t.Version, "attempt": t.CurrentAttempt, "kind": t.Kind},
		}); err != nil {
			return domain.Task{}, err
		}
		return t, nil
	})
}

func validateCreateTask(in CreateTaskInput) error {
	if in.MissionID == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Objective) == "" {
		return errors.New("mission, title, and objective are required")
	}
	switch in.Kind {
	case domain.TaskImplementation, domain.TaskScout, domain.TaskReview:
	default:
		return fmt.Errorf("unknown task kind %q", in.Kind)
	}
	if in.DeliveryMode == "" {
		return errors.New("delivery mode is required")
	}
	switch in.DeliveryMode {
	case domain.DeliveryGate, domain.DeliveryPR, domain.DeliveryBranch:
	default:
		return fmt.Errorf("unknown delivery mode %q", in.DeliveryMode)
	}
	return nil
}

type TransitionTaskInput struct {
	TaskID          domain.TaskID    `json:"task_id"`
	Attempt         int              `json:"attempt"`
	ExpectedState   domain.TaskState `json:"expected_state"`
	ExpectedVersion int64            `json:"expected_version"`
	To              domain.TaskState `json:"to"`
	Actor           string           `json:"actor"`
}

func (s *Store) TransitionTask(ctx context.Context, commandID domain.CommandID, in TransitionTaskInput) (domain.Task, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.Actor == "" {
		return domain.Task{}, errors.New("task, attempt, expected version, and actor are required")
	}
	return runCommand(ctx, s, commandID, "task.transition", in, func(tx *sql.Tx) (domain.Task, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		candidate := current
		candidate.State = in.ExpectedState
		if err := taskpolicy.ValidateTransition(candidate, in.To); err != nil {
			return domain.Task{}, err
		}
		if current.State == domain.TaskQueued && current.Version == in.ExpectedVersion &&
			current.CurrentAttempt == in.Attempt && in.ExpectedState == domain.TaskQueued &&
			in.To == domain.TaskProvisioning {
			mission, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", current.MissionID))
			if err != nil {
				return domain.Task{}, err
			}
			if mission.Budget.MaxWallClock > 0 && time.Since(mission.CreatedAt) >= mission.Budget.MaxWallClock {
				return expireTaskBudgetTx(ctx, tx, current, "mission.wall_clock", in.Actor, &commandID)
			}
			if mission.Budget.MaxConcurrentTasks > 0 {
				var active int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE mission_id = ? AND state IN (
					'provisioning', 'starting', 'running', 'blocked', 'collecting', 'ready',
					'validating', 'delivery_blocked')`, current.MissionID).Scan(&active); err != nil {
					return domain.Task{}, fmt.Errorf("count concurrent mission tasks: %w", err)
				}
				if active >= mission.Budget.MaxConcurrentTasks {
					return expireTaskBudgetTx(ctx, tx, current, "mission.concurrent_tasks", in.Actor, &commandID)
				}
			}
			openSignals, err := openSignalDependenciesTx(ctx, tx, in.TaskID)
			if err != nil {
				return domain.Task{}, err
			}
			if len(openSignals) != 0 {
				return domain.Task{}, &OpenSignalDependenciesError{SignalIDs: openSignals}
			}
		}

		now := time.Now().UTC()
		var completed any
		if taskpolicy.IsTerminal(in.To) {
			completed = formatTime(now)
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks
			SET state = ?, version = version + 1, updated_at = ?, completed_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`,
			in.To, formatTime(now), completed, in.TaskID, in.ExpectedState, in.ExpectedVersion, in.Attempt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("transition task: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return domain.Task{}, fmt.Errorf("count transition: %w", err)
		}
		if rows == 0 {
			reloaded, reloadErr := getTaskTx(ctx, tx, in.TaskID)
			if reloadErr != nil {
				return domain.Task{}, reloadErr
			}
			return domain.Task{}, &ConflictError{Current: reloaded}
		}
		if in.To == domain.TaskStarting {
			if _, err := tx.ExecContext(ctx, `UPDATE task_attempts
				SET started_at = COALESCE(started_at, ?)
				WHERE task_id = ? AND attempt = ?`, formatTime(now), in.TaskID, in.Attempt); err != nil {
				return domain.Task{}, fmt.Errorf("mark attempt started: %w", err)
			}
		}
		if taskpolicy.IsTerminal(in.To) {
			if _, err := tx.ExecContext(ctx, `UPDATE task_attempts
				SET completed_at = COALESCE(completed_at, ?)
				WHERE task_id = ? AND attempt = ?`, formatTime(now), in.TaskID, in.Attempt); err != nil {
				return domain.Task{}, fmt.Errorf("mark attempt completed: %w", err)
			}
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &updated.MissionID, TaskID: &updated.ID, Actor: in.Actor,
			Type: "task." + string(in.To), CommandID: &commandID,
			Payload: map[string]any{
				"from": in.ExpectedState, "to": in.To, "version": updated.Version, "attempt": in.Attempt,
			},
		}); err != nil {
			return domain.Task{}, err
		}
		if taskpolicy.IsTerminal(updated.State) {
			if _, err := regenerateMissionDigestTx(ctx, tx, updated.MissionID, "control-plane", "task."+string(updated.State), &commandID); err != nil {
				return domain.Task{}, err
			}
		}
		return updated, nil
	})
}

type RetryTaskInput struct {
	TaskID          domain.TaskID `json:"task_id"`
	ExpectedVersion int64         `json:"expected_version"`
	BaseSHA         string        `json:"base_sha,omitempty"`
	Branch          string        `json:"branch,omitempty"`
	Actor           string        `json:"actor"`
}

func (s *Store) RetryTask(ctx context.Context, commandID domain.CommandID, in RetryTaskInput) (domain.Task, error) {
	if in.TaskID == "" || in.ExpectedVersion < 1 || in.Actor == "" {
		return domain.Task{}, errors.New("task, expected version, and actor are required")
	}
	type retryResult struct {
		Task      domain.Task `json:"task"`
		Exhausted bool        `json:"exhausted"`
	}
	result, err := runCommand(ctx, s, commandID, "task.retry", in, func(tx *sql.Tx) (retryResult, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return retryResult{}, err
		}
		if current.Version != in.ExpectedVersion {
			return retryResult{}, &ConflictError{Current: current}
		}
		if !taskpolicy.IsRetryable(current.State) {
			return retryResult{}, ErrTaskNotRetryable
		}
		var maximum int
		if err := tx.QueryRowContext(ctx,
			"SELECT max_task_attempts FROM missions WHERE id = ?", current.MissionID).Scan(&maximum); err != nil {
			return retryResult{}, fmt.Errorf("load attempt budget: %w", err)
		}
		nextAttempt := current.CurrentAttempt + 1
		if maximum > 0 && nextAttempt > maximum {
			updated, err := expireRetryBudgetTx(ctx, tx, current, "mission.task_attempts", in.Actor, &commandID)
			return retryResult{Task: updated, Exhausted: true}, err
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE tasks
			SET state = ?, version = version + 1, current_attempt = ?, base_sha = ?, updated_at = ?, completed_at = NULL
			WHERE id = ? AND version = ? AND current_attempt = ?`,
			domain.TaskQueued, nextAttempt, nullableString(in.BaseSHA), formatTime(now),
			in.TaskID, in.ExpectedVersion, current.CurrentAttempt)
		if err != nil {
			return retryResult{}, fmt.Errorf("retry task: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return retryResult{}, fmt.Errorf("count retry update: %w", err)
		}
		if rows == 0 {
			reloaded, reloadErr := getTaskTx(ctx, tx, in.TaskID)
			if reloadErr != nil {
				return retryResult{}, reloadErr
			}
			return retryResult{}, &ConflictError{Current: reloaded}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_attempts
			SET completed_at = COALESCE(completed_at, ?)
			WHERE task_id = ? AND attempt = ?`, formatTime(now), in.TaskID, current.CurrentAttempt); err != nil {
			return retryResult{}, fmt.Errorf("close previous attempt: %w", err)
		}
		// A retry is an attempt fence, not merely a state reset.  A completion
		// holding the old lease can never satisfy the new current_attempt.
		if _, err := tx.ExecContext(ctx, `UPDATE treehouse_leases SET state = ?
			WHERE task_id = ? AND attempt = ? AND state = ?`, domain.TreehouseLeaseFenced,
			in.TaskID, current.CurrentAttempt, domain.TreehouseLeaseActive); err != nil {
			return retryResult{}, fmt.Errorf("fence previous attempt lease: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_attempts(task_id, attempt, base_sha, branch, created_at) VALUES (?, ?, ?, ?, ?)`,
			in.TaskID, nextAttempt, nullableString(in.BaseSHA), nullableString(in.Branch), formatTime(now)); err != nil {
			return retryResult{}, fmt.Errorf("insert retry attempt: %w", err)
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return retryResult{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &updated.MissionID, TaskID: &updated.ID, Actor: in.Actor,
			Type: "attempt.fenced", CommandID: &commandID,
			Payload: map[string]any{"attempt": current.CurrentAttempt, "reason": "retry"},
		}); err != nil {
			return retryResult{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &updated.MissionID, TaskID: &updated.ID, Actor: in.Actor,
			Type: "task.retry", CommandID: &commandID,
			Payload: map[string]any{"from_attempt": current.CurrentAttempt, "attempt": nextAttempt, "state": updated.State, "version": updated.Version},
		}); err != nil {
			return retryResult{}, err
		}
		return retryResult{Task: updated}, nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	if result.Exhausted {
		return result.Task, ErrAttemptBudget
	}
	return result.Task, nil
}

// CancelTask records both required cancellation transitions atomically. Cleanup
// (the external lease return and Herdr session stop) is deliberately outside
// this transaction so an unavailable runtime can never wedge task recovery.
func (s *Store) CancelTask(ctx context.Context, commandID domain.CommandID, taskID domain.TaskID, expectedVersion int64, actor string) (domain.Task, error) {
	if taskID == "" || expectedVersion < 1 || actor == "" {
		return domain.Task{}, errors.New("task, expected version, and actor are required")
	}
	request := struct {
		TaskID          domain.TaskID `json:"task_id"`
		ExpectedVersion int64         `json:"expected_version"`
		Actor           string        `json:"actor"`
	}{taskID, expectedVersion, actor}
	return runCommand(ctx, s, commandID, "task.cancel", request, func(tx *sql.Tx) (domain.Task, error) {
		current, err := getTaskTx(ctx, tx, taskID)
		if err != nil {
			return domain.Task{}, err
		}
		if taskpolicy.IsTerminal(current.State) || current.Version != expectedVersion {
			return domain.Task{}, &ConflictError{Current: current}
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskCancelling,
			formatTime(now), taskID, current.State, current.Version, current.CurrentAttempt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("begin cancellation: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.Task{}, &ConflictError{Current: current}
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID, Actor: actor,
			Type: "task.cancelling", CommandID: &commandID, Payload: map[string]any{"from": current.State, "to": domain.TaskCancelling, "attempt": current.CurrentAttempt}}); err != nil {
			return domain.Task{}, err
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?, completed_at = ?
			WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskCancelled,
			formatTime(now), formatTime(now), taskID, domain.TaskCancelling, current.Version+1, current.CurrentAttempt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("finish cancellation: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.Task{}, &ConflictError{Current: current}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_attempts SET completed_at = COALESCE(completed_at, ?) WHERE task_id = ? AND attempt = ?`, formatTime(now), taskID, current.CurrentAttempt); err != nil {
			return domain.Task{}, fmt.Errorf("close cancelled attempt: %w", err)
		}
		updated, err := getTaskTx(ctx, tx, taskID)
		if err != nil {
			return domain.Task{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID, Actor: actor,
			Type: "task.cancelled", CommandID: &commandID, Payload: map[string]any{"from": domain.TaskCancelling, "to": domain.TaskCancelled, "attempt": current.CurrentAttempt}}); err != nil {
			return domain.Task{}, err
		}
		return updated, nil
	})
}

func (s *Store) Task(ctx context.Context, taskID domain.TaskID) (domain.Task, error) {
	return getTaskQuery(ctx, s.db, taskID)
}

func (s *Store) Attempt(ctx context.Context, taskID domain.TaskID, attempt int) (domain.TaskAttempt, error) {
	row := s.db.QueryRowContext(ctx, `SELECT task_id, attempt, base_sha, head_sha, branch, worktree_path,
		treehouse_lease_id, treehouse_lease_holder, worker_session_id, result_path, result_sha256,
		result_json, created_at, started_at, completed_at
		FROM task_attempts WHERE task_id = ? AND attempt = ?`, taskID, attempt)
	return scanAttempt(row)
}

type rowScanner interface {
	Scan(...any) error
}

type taskQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const taskSelectMany = `SELECT id, mission_id, parent_task_id, base_task_id, base_sha, kind, title, objective, acceptance_criteria_json,
	state, version, priority, worker_agent, delivery_mode, current_attempt, created_at, updated_at, completed_at
	FROM tasks`

const taskSelect = taskSelectMany + ` WHERE id = ?`

func getTaskTx(ctx context.Context, tx *sql.Tx, taskID domain.TaskID) (domain.Task, error) {
	return scanTask(tx.QueryRowContext(ctx, taskSelect, taskID))
}

func getTaskQuery(ctx context.Context, q taskQuerier, taskID domain.TaskID) (domain.Task, error) {
	return scanTask(q.QueryRowContext(ctx, taskSelect, taskID))
}

func scanTask(row rowScanner) (domain.Task, error) {
	var t domain.Task
	var parent, baseTask, baseSHA, worker, created, updated, completed sql.NullString
	var criteria []byte
	if err := row.Scan(&t.ID, &t.MissionID, &parent, &baseTask, &baseSHA, &t.Kind, &t.Title, &t.Objective,
		&criteria, &t.State, &t.Version, &t.Priority, &worker, &t.DeliveryMode, &t.CurrentAttempt,
		&created, &updated, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, ErrNotFound
		}
		return domain.Task{}, fmt.Errorf("scan task: %w", err)
	}
	if parent.Valid {
		value := domain.TaskID(parent.String)
		t.ParentTaskID = &value
	}
	if baseTask.Valid {
		value := domain.TaskID(baseTask.String)
		t.BaseTaskID = &value
	}
	t.BaseSHA = baseSHA.String
	t.WorkerAgent = worker.String
	if err := json.Unmarshal(criteria, &t.AcceptanceCriteria); err != nil {
		return domain.Task{}, fmt.Errorf("decode task acceptance criteria: %w", err)
	}
	var err error
	t.CreatedAt, err = parseTime(created.String)
	if err == nil {
		t.UpdatedAt, err = parseTime(updated.String)
	}
	if err == nil && completed.Valid {
		t.CompletedAt, err = parseOptionalTime(&completed.String)
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("parse task timestamp: %w", err)
	}
	return t, nil
}

func scanAttempt(row rowScanner) (domain.TaskAttempt, error) {
	var a domain.TaskAttempt
	var baseSHA, headSHA, branch, worktree, leaseID, leaseHolder, worker, resultPath, resultSHA, resultJSON sql.NullString
	var created, started, completed sql.NullString
	if err := row.Scan(&a.TaskID, &a.Attempt, &baseSHA, &headSHA, &branch, &worktree,
		&leaseID, &leaseHolder, &worker, &resultPath, &resultSHA, &resultJSON,
		&created, &started, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TaskAttempt{}, ErrNotFound
		}
		return domain.TaskAttempt{}, fmt.Errorf("scan attempt: %w", err)
	}
	a.BaseSHA, a.HeadSHA, a.Branch, a.WorktreePath = baseSHA.String, headSHA.String, branch.String, worktree.String
	a.TreehouseLeaseID, a.TreehouseLeaseHolder = leaseID.String, leaseHolder.String
	a.WorkerSessionID = domain.SessionID(worker.String)
	a.ResultPath, a.ResultSHA256 = resultPath.String, resultSHA.String
	if resultJSON.Valid {
		var result domain.WorkerResult
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return domain.TaskAttempt{}, fmt.Errorf("decode worker result: %w", err)
		}
		a.Result = &result
	}
	var err error
	a.CreatedAt, err = parseTime(created.String)
	if err == nil && started.Valid {
		a.StartedAt, err = parseOptionalTime(&started.String)
	}
	if err == nil && completed.Valid {
		a.CompletedAt, err = parseOptionalTime(&completed.String)
	}
	if err != nil {
		return domain.TaskAttempt{}, fmt.Errorf("parse attempt timestamp: %w", err)
	}
	return a, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func taskIDValue(value *domain.TaskID) any {
	if value == nil {
		return nil
	}
	return *value
}
