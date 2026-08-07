package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
	signalpolicy "parallel-intellect/internal/signals"
)

const operatorCancellationNote = "Cancelled by operator."

// BeginMissionCancel fences a mission against further task creation before
// callers run the existing per-task cancellation and lease cleanup path.
func (s *Store) BeginMissionCancel(ctx context.Context, commandID domain.CommandID, missionID domain.MissionID, actor string) (domain.Mission, error) {
	if missionID == "" || strings.TrimSpace(actor) == "" {
		return domain.Mission{}, errors.New("mission and actor are required")
	}
	return runCommand(ctx, s, commandID, "mission.cancel.begin", struct {
		MissionID domain.MissionID `json:"mission_id"`
		Actor     string           `json:"actor"`
	}{missionID, actor}, func(tx *sql.Tx) (domain.Mission, error) {
		current, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", missionID))
		if err != nil {
			return domain.Mission{}, err
		}
		if current.State == domain.MissionCancelled {
			return current, nil
		}
		if current.State == domain.MissionCancelling {
			return current, nil
		}
		if current.State != domain.MissionActive && current.State != domain.MissionCompleting {
			return domain.Mission{}, fmt.Errorf("cannot cancel mission in state %q", current.State)
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE missions SET state = ?, version = version + 1
			WHERE id = ? AND state = ? AND version = ?`, domain.MissionCancelling, missionID, current.State, current.Version)
		if err != nil {
			return domain.Mission{}, fmt.Errorf("begin mission cancellation: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.Mission{}, errors.New("stale mission cancellation")
		}
		updated, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", missionID))
		if err != nil {
			return domain.Mission{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, Actor: actor, Type: "mission.cancelling", CommandID: &commandID,
			Payload: map[string]any{"from": current.State, "to": updated.State, "version": updated.Version, "at": formatTime(now)}}); err != nil {
			return domain.Mission{}, err
		}
		return updated, nil
	})
}

// FinishMissionCancel closes operator questions, stops a bound commander in
// durable state (a dead runtime is intentionally tolerated), and makes the
// mission terminal only after task cancellation has completed.
func (s *Store) FinishMissionCancel(ctx context.Context, commandID domain.CommandID, missionID domain.MissionID, actor string) (domain.Mission, error) {
	if missionID == "" || strings.TrimSpace(actor) == "" {
		return domain.Mission{}, errors.New("mission and actor are required")
	}
	return runCommand(ctx, s, commandID, "mission.cancel", struct {
		MissionID domain.MissionID `json:"mission_id"`
		Actor     string           `json:"actor"`
	}{missionID, actor}, func(tx *sql.Tx) (domain.Mission, error) {
		current, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", missionID))
		if err != nil {
			return domain.Mission{}, err
		}
		if current.State == domain.MissionCancelled {
			return current, nil
		}
		if current.State != domain.MissionCancelling {
			return domain.Mission{}, fmt.Errorf("mission is not cancelling: %q", current.State)
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE mission_id = ? AND state NOT IN ('delivered', 'delivered_branch', 'report_ready', 'cancelled', 'failed')`, missionID).Scan(&active); err != nil {
			return domain.Mission{}, fmt.Errorf("count active mission tasks: %w", err)
		}
		if active != 0 {
			return domain.Mission{}, errors.New("mission cancellation has active tasks")
		}
		now := time.Now().UTC()
		rows, err := tx.QueryContext(ctx, `SELECT id, task_id, version FROM signals WHERE mission_id = ? AND status = ?`, missionID, signalpolicy.SignalOpen)
		if err != nil {
			return domain.Mission{}, fmt.Errorf("list open mission signals: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var signalID signalpolicy.SignalID
			var taskID sql.NullString
			var version int64
			if err := rows.Scan(&signalID, &taskID, &version); err != nil {
				return domain.Mission{}, err
			}
			result, err := tx.ExecContext(ctx, `UPDATE signals SET status = ?, answer = ?, version = version + 1, resolved_at = ? WHERE id = ? AND status = ? AND version = ?`, signalpolicy.SignalResolved, operatorCancellationNote, formatTime(now), signalID, signalpolicy.SignalOpen, version)
			if err != nil {
				return domain.Mission{}, fmt.Errorf("close cancellation signal: %w", err)
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				return domain.Mission{}, errors.New("stale cancellation signal")
			}
			var eventTaskID *domain.TaskID
			if taskID.Valid {
				value := domain.TaskID(taskID.String)
				eventTaskID = &value
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, TaskID: eventTaskID, Actor: actor, Type: "signal.resolved", CommandID: &commandID,
				Payload: map[string]any{"signal_id": signalID, "answer": operatorCancellationNote, "status": signalpolicy.SignalResolved, "reason": "mission_cancelled"}}); err != nil {
				return domain.Mission{}, err
			}
		}
		if err := rows.Err(); err != nil {
			return domain.Mission{}, err
		}
		if current.CommanderSessionID != "" {
			result, err := tx.ExecContext(ctx, `UPDATE commander_sessions SET state = 'stopped', version = version + 1, updated_at = ?, stopped_at = COALESCE(stopped_at, ?), failure_reason = COALESCE(failure_reason, ?)
				WHERE id = ? AND mission_id = ? AND state NOT IN ('stopped', 'failed')`, formatTime(now), formatTime(now), "mission cancelled by operator", current.CommanderSessionID, missionID)
			if err != nil {
				return domain.Mission{}, fmt.Errorf("stop commander for mission cancellation: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil || changed > 1 {
				return domain.Mission{}, errors.New("stale commander cancellation")
			}
			if changed == 1 {
				if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, Actor: actor, Type: "commander.session.stopped", CommandID: &commandID,
					Payload: map[string]any{"commander_session_id": current.CommanderSessionID, "reason": "mission_cancelled"}}); err != nil {
					return domain.Mission{}, err
				}
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE missions SET state = ?, version = version + 1, completed_at = ? WHERE id = ? AND state = ? AND version = ?`, domain.MissionCancelled, formatTime(now), missionID, domain.MissionCancelling, current.Version)
		if err != nil {
			return domain.Mission{}, fmt.Errorf("finish mission cancellation: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return domain.Mission{}, errors.New("stale mission cancellation")
		}
		updated, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", missionID))
		if err != nil {
			return domain.Mission{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, Actor: actor, Type: "mission.cancelled", CommandID: &commandID,
			Payload: map[string]any{"from": current.State, "to": updated.State, "version": updated.Version}}); err != nil {
			return domain.Mission{}, err
		}
		if _, err := regenerateMissionDigestTx(ctx, tx, missionID, "control-plane", "mission.cancelled", &commandID); err != nil {
			return domain.Mission{}, err
		}
		return updated, nil
	})
}
