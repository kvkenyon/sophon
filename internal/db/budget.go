package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"parallel-intellect/internal/domain"
	taskpolicy "parallel-intellect/internal/task"
)

type BudgetExpiration struct {
	Scope     string           `json:"scope"`
	Dimension string           `json:"dimension"`
	MissionID domain.MissionID `json:"mission_id,omitempty"`
	TaskID    domain.TaskID    `json:"task_id,omitempty"`
	SessionID domain.SessionID `json:"session_id,omitempty"`
	Expired   bool             `json:"expired"`
}

type EnforceMissionBudgetInput struct {
	MissionID  domain.MissionID `json:"mission_id"`
	ObservedAt time.Time        `json:"observed_at"`
	Actor      string           `json:"actor"`
}

// EnforceMissionBudget is the daemon-safe wall-clock sweep. It moves every
// affected live task and commander session to needs_attention atomically.
func (s *Store) EnforceMissionBudget(ctx context.Context, commandID domain.CommandID, in EnforceMissionBudgetInput) (BudgetExpiration, error) {
	if in.MissionID == "" || in.Actor == "" {
		return BudgetExpiration{}, errors.New("mission and actor are required")
	}
	if in.ObservedAt.IsZero() {
		in.ObservedAt = time.Now().UTC()
	}
	return runCommand(ctx, s, commandID, "budget.mission.enforce", in, func(tx *sql.Tx) (BudgetExpiration, error) {
		mission, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", in.MissionID))
		if err != nil {
			return BudgetExpiration{}, err
		}
		result := BudgetExpiration{Scope: "mission", Dimension: "wall_clock", MissionID: mission.ID}
		if mission.Budget.MaxWallClock <= 0 || in.ObservedAt.Sub(mission.CreatedAt) < mission.Budget.MaxWallClock {
			return result, nil
		}
		result.Expired = true
		rows, err := tx.QueryContext(ctx, taskSelectMany+" WHERE mission_id = ? ORDER BY created_at, id", mission.ID)
		if err != nil {
			return BudgetExpiration{}, err
		}
		var tasks []domain.Task
		for rows.Next() {
			task, scanErr := scanTask(rows)
			if scanErr != nil {
				rows.Close()
				return BudgetExpiration{}, scanErr
			}
			tasks = append(tasks, task)
		}
		if err := rows.Close(); err != nil {
			return BudgetExpiration{}, err
		}
		for _, task := range tasks {
			if taskpolicy.IsTerminal(task.State) || task.State == domain.TaskNeedsAttention {
				continue
			}
			if _, err := expireTaskBudgetTx(ctx, tx, task, "mission.wall_clock", in.Actor, &commandID); err != nil {
				return BudgetExpiration{}, err
			}
		}
		var sessionID domain.SessionID
		var state domain.CommanderSessionState
		var version int64
		err = tx.QueryRowContext(ctx, `SELECT id, state, version FROM commander_sessions WHERE mission_id = ?`, mission.ID).Scan(&sessionID, &state, &version)
		if err == nil && state != domain.CommanderSessionNeedsAttention && state != domain.CommanderSessionStopped && state != domain.CommanderSessionFailed {
			if _, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET state = ?, version = version + 1,
				updated_at = ?, failure_reason = ? WHERE id = ? AND version = ?`, domain.CommanderSessionNeedsAttention,
				formatTime(in.ObservedAt), "budget exhausted: mission.wall_clock", sessionID, version); err != nil {
				return BudgetExpiration{}, err
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: &mission.ID, Actor: in.Actor,
				Type: "budget.exhausted", CommandID: &commandID, Payload: map[string]any{
					"scope": "commander", "dimension": "mission.wall_clock", "session_id": sessionID}}); err != nil {
				return BudgetExpiration{}, err
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return BudgetExpiration{}, err
		}
		return result, nil
	})
}

type ReserveWorkerBudgetInput struct {
	TaskID          domain.TaskID    `json:"task_id"`
	Attempt         int              `json:"attempt"`
	SessionID       domain.SessionID `json:"session_id"`
	ExpectedVersion int64            `json:"expected_version"`
	Dimension       string           `json:"dimension"`
	ObservedAt      time.Time        `json:"observed_at"`
	Actor           string           `json:"actor"`
}

// ReserveWorkerBudget consumes a restart or fix round before autonomous work.
// It always checks worker runtime first and fences exhaustion into task state.
func (s *Store) ReserveWorkerBudget(ctx context.Context, commandID domain.CommandID, in ReserveWorkerBudgetInput) (domain.Task, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.SessionID == "" || in.ExpectedVersion < 1 || in.Actor == "" {
		return domain.Task{}, errors.New("complete worker budget reservation is required")
	}
	if in.ObservedAt.IsZero() {
		in.ObservedAt = time.Now().UTC()
	}
	if in.Dimension != "restart" && in.Dimension != "fix_round" && in.Dimension != "runtime" {
		return domain.Task{}, fmt.Errorf("unknown worker budget dimension %q", in.Dimension)
	}
	return runCommand(ctx, s, commandID, "budget.worker.reserve", in, func(tx *sql.Tx) (domain.Task, error) {
		session, err := getWorkerSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.Task{}, err
		}
		task, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if session.TaskID != task.ID || session.Attempt != in.Attempt || session.Version != in.ExpectedVersion || task.CurrentAttempt != in.Attempt {
			return domain.Task{}, errors.New("stale worker budget reservation")
		}
		dimension := in.Dimension
		exhausted := session.Budget.MaxRuntime > 0 && in.ObservedAt.Sub(session.CreatedAt) >= session.Budget.MaxRuntime
		if exhausted {
			dimension = "runtime"
		}
		if !exhausted && in.Dimension == "restart" {
			exhausted = session.Budget.MaxRestarts > 0 && session.RestartCount >= session.Budget.MaxRestarts
		}
		if !exhausted && in.Dimension == "fix_round" {
			exhausted = session.Budget.MaxFixRounds > 0 && session.FixRoundCount >= session.Budget.MaxFixRounds
		}
		if exhausted {
			return expireTaskBudgetTx(ctx, tx, task, "worker."+dimension, in.Actor, &commandID)
		}
		if in.Dimension == "restart" {
			_, err = tx.ExecContext(ctx, `UPDATE worker_sessions SET restart_count = restart_count + 1
				WHERE id = ? AND version = ?`, session.ID, session.Version)
		} else if in.Dimension == "fix_round" {
			_, err = tx.ExecContext(ctx, `UPDATE worker_sessions SET fix_round_count = fix_round_count + 1
				WHERE id = ? AND version = ?`, session.ID, session.Version)
		}
		if err != nil {
			return domain.Task{}, err
		}
		return task, nil
	})
}

type ReserveCommanderTurnInput struct {
	MissionID       domain.MissionID `json:"mission_id"`
	SessionID       domain.SessionID `json:"session_id"`
	ExpectedVersion int64            `json:"expected_version"`
	ObservedAt      time.Time        `json:"observed_at"`
	Actor           string           `json:"actor"`
}

func (s *Store) ReserveCommanderTurn(ctx context.Context, commandID domain.CommandID, in ReserveCommanderTurnInput) (domain.CommanderSession, error) {
	if in.MissionID == "" || in.SessionID == "" || in.ExpectedVersion < 1 || in.Actor == "" {
		return domain.CommanderSession{}, errors.New("complete commander turn reservation is required")
	}
	if in.ObservedAt.IsZero() {
		in.ObservedAt = time.Now().UTC()
	}
	return runCommand(ctx, s, commandID, "budget.commander.turn", in, func(tx *sql.Tx) (domain.CommanderSession, error) {
		session, err := getCommanderSessionTx(ctx, tx, in.SessionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if session.MissionID != in.MissionID || session.Version != in.ExpectedVersion {
			return domain.CommanderSession{}, errors.New("stale commander turn reservation")
		}
		dimension := "turns"
		exhausted := session.Budget.MaxTurns > 0 && session.TurnCount >= session.Budget.MaxTurns
		if session.Budget.MaxDuration > 0 && in.ObservedAt.Sub(session.BudgetStartedAt) >= session.Budget.MaxDuration {
			dimension, exhausted = "duration", true
		}
		if exhausted {
			if session.State != domain.CommanderSessionNeedsAttention {
				if _, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET state = ?, version = version + 1,
					updated_at = ?, failure_reason = ? WHERE id = ? AND version = ?`, domain.CommanderSessionNeedsAttention,
					formatTime(in.ObservedAt), "budget exhausted: commander."+dimension, session.ID, session.Version); err != nil {
					return domain.CommanderSession{}, err
				}
				if err := appendEvent(ctx, tx, eventInput{MissionID: &session.MissionID, Actor: in.Actor, Type: "budget.exhausted",
					CommandID: &commandID, Payload: map[string]any{"scope": "commander", "dimension": dimension,
						"session_id": session.ID}}); err != nil {
					return domain.CommanderSession{}, err
				}
			}
			return getCommanderSessionTx(ctx, tx, session.ID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET turn_count = turn_count + 1,
			version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, formatTime(in.ObservedAt), session.ID, session.Version); err != nil {
			return domain.CommanderSession{}, err
		}
		return getCommanderSessionTx(ctx, tx, session.ID)
	})
}

func expireTaskBudgetTx(ctx context.Context, tx *sql.Tx, task domain.Task, dimension, actor string, commandID *domain.CommandID) (domain.Task, error) {
	return expireTaskBudget(ctx, tx, task, dimension, actor, commandID, false)
}

func expireRetryBudgetTx(ctx context.Context, tx *sql.Tx, task domain.Task, dimension, actor string, commandID *domain.CommandID) (domain.Task, error) {
	return expireTaskBudget(ctx, tx, task, dimension, actor, commandID, true)
}

func expireTaskBudget(ctx context.Context, tx *sql.Tx, task domain.Task, dimension, actor string, commandID *domain.CommandID, includeTerminal bool) (domain.Task, error) {
	if task.State == domain.TaskNeedsAttention || (taskpolicy.IsTerminal(task.State) && !includeTerminal) {
		return task, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskNeedsAttention,
		formatTime(time.Now().UTC()), task.ID, task.State, task.Version, task.CurrentAttempt); err != nil {
		return domain.Task{}, err
	}
	updated, err := getTaskTx(ctx, tx, task.ID)
	if err != nil {
		return domain.Task{}, err
	}
	if err := appendEvent(ctx, tx, eventInput{MissionID: &task.MissionID, TaskID: &task.ID, Actor: actor,
		Type: "budget.exhausted", CommandID: commandID, Payload: map[string]any{"scope": "task",
			"dimension": dimension, "attempt": task.CurrentAttempt, "from": task.State,
			"to": domain.TaskNeedsAttention, "version": updated.Version}}); err != nil {
		return domain.Task{}, err
	}
	return updated, nil
}
