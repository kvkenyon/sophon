package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sophon/internal/domain"
)

// UpdateMissionBudgetInput replaces a mission's complete budget under the
// supplied mission version. Zero values are deliberately unbounded.
type UpdateMissionBudgetInput struct {
	MissionID       domain.MissionID     `json:"mission_id"`
	ExpectedVersion int64                `json:"expected_version"`
	Budget          domain.MissionBudget `json:"budget"`
	Actor           string               `json:"actor"`
}

// MissionBudgetUpdate is the command result. RecoverableTaskIDs are left in
// needs_attention intentionally: an operator or commander still explicitly
// chooses whether to retry each task.
type MissionBudgetUpdate struct {
	Mission            domain.Mission  `json:"mission"`
	RecoverableTaskIDs []domain.TaskID `json:"recoverable_task_ids"`
}

// UpdateMissionBudget changes opt-in mission limits atomically with its event.
// It also identifies tasks parked by a now-nonbinding mission budget so callers
// can retry them normally without database surgery.
func (s *Store) UpdateMissionBudget(ctx context.Context, commandID domain.CommandID, in UpdateMissionBudgetInput) (MissionBudgetUpdate, error) {
	if in.MissionID == "" || in.ExpectedVersion < 0 || in.Actor == "" {
		return MissionBudgetUpdate{}, errors.New("mission, non-negative expected version, and actor are required")
	}
	return runCommand(ctx, s, commandID, "mission.budget.update", in, func(tx *sql.Tx) (MissionBudgetUpdate, error) {
		current, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", in.MissionID))
		if err != nil {
			return MissionBudgetUpdate{}, err
		}
		expectedVersion := in.ExpectedVersion
		if expectedVersion == 0 {
			expectedVersion = current.Version
		}
		if current.Version != expectedVersion {
			return MissionBudgetUpdate{}, fmt.Errorf("stale mission budget update")
		}
		result, err := tx.ExecContext(ctx, `UPDATE missions SET
			max_wall_clock_ns = ?, max_concurrent_tasks = ?, max_task_attempts = ?,
			max_validation_runs = ?, max_tokens = ?, max_cost = ?, version = version + 1
			WHERE id = ? AND version = ?`, int64(in.Budget.MaxWallClock), in.Budget.MaxConcurrentTasks,
			in.Budget.MaxTaskAttempts, in.Budget.MaxValidationRuns, in.Budget.MaxTokens, in.Budget.MaxCost,
			in.MissionID, expectedVersion)
		if err != nil {
			return MissionBudgetUpdate{}, fmt.Errorf("update mission budget: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return MissionBudgetUpdate{}, errors.New("stale mission budget update")
		}
		updated, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", in.MissionID))
		if err != nil {
			return MissionBudgetUpdate{}, err
		}
		recoverable, err := recoverableBudgetTasksTx(ctx, tx, updated)
		if err != nil {
			return MissionBudgetUpdate{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.ID, Actor: in.Actor, Type: "mission.budget.updated", CommandID: &commandID,
			Payload: map[string]any{"previous": current.Budget, "budget": updated.Budget, "version": updated.Version, "recoverable_task_ids": recoverable}}); err != nil {
			return MissionBudgetUpdate{}, err
		}
		return MissionBudgetUpdate{Mission: updated, RecoverableTaskIDs: recoverable}, nil
	})
}

func recoverableBudgetTasksTx(ctx context.Context, tx *sql.Tx, mission domain.Mission) ([]domain.TaskID, error) {
	rows, err := tx.QueryContext(ctx, taskSelectMany+" WHERE mission_id = ? AND state = ? ORDER BY created_at, id", mission.ID, domain.TaskNeedsAttention)
	if err != nil {
		return nil, fmt.Errorf("list budget-paused tasks: %w", err)
	}
	defer rows.Close()
	var tasks []domain.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var recoverable []domain.TaskID
	for _, task := range tasks {
		var payload []byte
		err = tx.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE task_id = ? AND type = 'budget.exhausted' ORDER BY sequence DESC LIMIT 1`, task.ID).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load task budget exhaustion: %w", err)
		}
		var event struct {
			Dimension string `json:"dimension"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("decode task budget exhaustion: %w", err)
		}
		legal, err := missionBudgetNowAllowsRetryTx(ctx, tx, mission, task, event.Dimension)
		if err != nil {
			return nil, err
		}
		if legal {
			recoverable = append(recoverable, task.ID)
		}
	}
	return recoverable, nil
}

func missionBudgetNowAllowsRetryTx(ctx context.Context, tx *sql.Tx, mission domain.Mission, task domain.Task, dimension string) (bool, error) {
	switch dimension {
	case "mission.wall_clock":
		return mission.Budget.MaxWallClock <= 0 || time.Since(mission.CreatedAt) < mission.Budget.MaxWallClock, nil
	case "mission.task_attempts":
		return mission.Budget.MaxTaskAttempts <= 0 || task.CurrentAttempt+1 <= mission.Budget.MaxTaskAttempts, nil
	case "mission.concurrent_tasks":
		if mission.Budget.MaxConcurrentTasks <= 0 {
			return true, nil
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE mission_id = ? AND state IN (
			'provisioning', 'starting', 'running', 'blocked', 'collecting', 'ready', 'validating', 'delivery_blocked')`, mission.ID).Scan(&active); err != nil {
			return false, fmt.Errorf("count concurrent mission tasks: %w", err)
		}
		return active < mission.Budget.MaxConcurrentTasks, nil
	case "mission.validation_rounds":
		if mission.Budget.MaxValidationRuns <= 0 {
			return true, nil
		}
		var used int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE task_id = ? AND type = 'validation.started'", task.ID).Scan(&used); err != nil {
			return false, fmt.Errorf("count validation rounds: %w", err)
		}
		return used < mission.Budget.MaxValidationRuns, nil
	default:
		return false, nil
	}
}
