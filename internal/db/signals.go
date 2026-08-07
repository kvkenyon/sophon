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
	signalpolicy "parallel-intellect/internal/signals"
)

var (
	ErrSignalMissionMismatch  = errors.New("task and signal must belong to the same mission")
	ErrOpenSignalDependencies = errors.New("task has unresolved signal dependencies")
)

// SignalConflictError reports the authoritative signal after a failed CAS.
type SignalConflictError struct {
	Current signalpolicy.Signal
}

func (e *SignalConflictError) Error() string { return "stale signal transition" }

// OpenSignalDependenciesError identifies the open signals gating a task.
type OpenSignalDependenciesError struct {
	SignalIDs []signalpolicy.SignalID
}

func (e *OpenSignalDependenciesError) Error() string {
	return fmt.Sprintf("%s: %v", ErrOpenSignalDependencies, e.SignalIDs)
}

func (e *OpenSignalDependenciesError) Unwrap() error { return ErrOpenSignalDependencies }

type CreateSignalInput struct {
	MissionID      domain.MissionID        `json:"mission_id"`
	TaskID         *domain.TaskID          `json:"task_id,omitempty"`
	Kind           signalpolicy.SignalKind `json:"kind"`
	Question       string                  `json:"question"`
	Context        string                  `json:"context,omitempty"`
	Options        []signalpolicy.Option   `json:"options,omitempty"`
	Recommendation string                  `json:"recommendation,omitempty"`
	Actor          string                  `json:"actor"`
}

// CreateSignal gives an unresolved operator question durable identity. The
// command record, projection, and signal.created event commit atomically.
func (s *Store) CreateSignal(ctx context.Context, commandID domain.CommandID, in CreateSignalInput) (signalpolicy.Signal, error) {
	if err := signalpolicy.ValidateNew(in.MissionID, in.TaskID, in.Kind, in.Question); err != nil {
		return signalpolicy.Signal{}, err
	}
	if strings.TrimSpace(in.Actor) == "" {
		return signalpolicy.Signal{}, errors.New("actor is required")
	}
	return runCommand(ctx, s, commandID, "signal.create", in, func(tx *sql.Tx) (signalpolicy.Signal, error) {
		if in.TaskID != nil {
			if err := verifyTaskMission(ctx, tx, *in.TaskID, in.MissionID); err != nil {
				return signalpolicy.Signal{}, err
			}
		}
		rawID, err := id.New("sig")
		if err != nil {
			return signalpolicy.Signal{}, err
		}
		options := in.Options
		if options == nil {
			options = []signalpolicy.Option{}
		}
		optionsJSON, err := json.Marshal(options)
		if err != nil {
			return signalpolicy.Signal{}, fmt.Errorf("encode signal options: %w", err)
		}
		now := time.Now().UTC()
		signal := signalpolicy.Signal{
			ID: signalpolicy.SignalID(rawID), MissionID: in.MissionID, TaskID: in.TaskID,
			Kind: in.Kind, Question: in.Question, Context: in.Context, Options: options,
			Recommendation: in.Recommendation, Status: signalpolicy.SignalOpen,
			Version: 1, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO signals(
			id, mission_id, task_id, kind, question, context, options_json,
			recommendation, status, version, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			signal.ID, signal.MissionID, taskIDValue(signal.TaskID), signal.Kind,
			signal.Question, signal.Context, optionsJSON, signal.Recommendation,
			signal.Status, signal.Version, formatTime(now)); err != nil {
			return signalpolicy.Signal{}, fmt.Errorf("insert signal: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &signal.MissionID, TaskID: signal.TaskID, Actor: in.Actor,
			Type: "signal.created", CommandID: &commandID,
			Payload: map[string]any{
				"signal_id": signal.ID, "kind": signal.Kind,
				"status": signal.Status, "version": signal.Version,
			},
		}); err != nil {
			return signalpolicy.Signal{}, err
		}
		return signal, nil
	})
}

type ResolveSignalInput struct {
	SignalID        signalpolicy.SignalID `json:"signal_id"`
	ExpectedVersion int64                 `json:"expected_version"`
	Answer          string                `json:"answer"`
	Actor           string                `json:"actor"`
}

// ResolveSignal records the operator's answer with a compare-and-swap state
// transition and appends signal.resolved in the same transaction.
func (s *Store) ResolveSignal(ctx context.Context, commandID domain.CommandID, in ResolveSignalInput) (signalpolicy.Signal, error) {
	if in.SignalID == "" || in.ExpectedVersion < 1 || strings.TrimSpace(in.Answer) == "" || strings.TrimSpace(in.Actor) == "" {
		return signalpolicy.Signal{}, errors.New("signal, expected version, answer, and actor are required")
	}
	return runCommand(ctx, s, commandID, "signal.resolve", in, func(tx *sql.Tx) (signalpolicy.Signal, error) {
		current, err := getSignalTx(ctx, tx, in.SignalID)
		if err != nil {
			return signalpolicy.Signal{}, err
		}
		if err := signalpolicy.ValidateTransition(current.Status, signalpolicy.SignalResolved); err != nil {
			return signalpolicy.Signal{}, err
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE signals
			SET status = ?, answer = ?, version = version + 1, resolved_at = ?
			WHERE id = ? AND status = ? AND version = ?`,
			signalpolicy.SignalResolved, in.Answer, formatTime(now), in.SignalID,
			signalpolicy.SignalOpen, in.ExpectedVersion)
		if err != nil {
			return signalpolicy.Signal{}, fmt.Errorf("resolve signal: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return signalpolicy.Signal{}, fmt.Errorf("count signal resolution: %w", err)
		}
		if rows == 0 {
			reloaded, reloadErr := getSignalTx(ctx, tx, in.SignalID)
			if reloadErr != nil {
				return signalpolicy.Signal{}, reloadErr
			}
			return signalpolicy.Signal{}, &SignalConflictError{Current: reloaded}
		}
		resolved, err := getSignalTx(ctx, tx, in.SignalID)
		if err != nil {
			return signalpolicy.Signal{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &resolved.MissionID, TaskID: resolved.TaskID, Actor: in.Actor,
			Type: "signal.resolved", CommandID: &commandID,
			Payload: map[string]any{
				"signal_id": resolved.ID, "answer": in.Answer,
				"status": resolved.Status, "version": resolved.Version,
			},
		}); err != nil {
			return signalpolicy.Signal{}, err
		}
		if _, err := regenerateMissionDigestTx(ctx, tx, resolved.MissionID, "control-plane", "signal.resolved", &commandID); err != nil {
			return signalpolicy.Signal{}, err
		}
		return resolved, nil
	})
}

type AddTaskSignalDependencyInput struct {
	TaskID   domain.TaskID         `json:"task_id"`
	SignalID signalpolicy.SignalID `json:"signal_id"`
	Actor    string                `json:"actor"`
}

// AddTaskSignalDependency makes task provisioning conditional on the signal
// being resolved. Repeating the same command returns the original linkage.
func (s *Store) AddTaskSignalDependency(ctx context.Context, commandID domain.CommandID, in AddTaskSignalDependencyInput) (AddTaskSignalDependencyInput, error) {
	if in.TaskID == "" || in.SignalID == "" || strings.TrimSpace(in.Actor) == "" {
		return AddTaskSignalDependencyInput{}, errors.New("task, signal, and actor are required")
	}
	return runCommand(ctx, s, commandID, "task.signal_dependency.add", in, func(tx *sql.Tx) (AddTaskSignalDependencyInput, error) {
		var taskMission, signalMission domain.MissionID
		if err := tx.QueryRowContext(ctx, "SELECT mission_id FROM tasks WHERE id = ?", in.TaskID).Scan(&taskMission); err != nil {
			return AddTaskSignalDependencyInput{}, mapNotFound("load dependency task", err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT mission_id FROM signals WHERE id = ?", in.SignalID).Scan(&signalMission); err != nil {
			return AddTaskSignalDependencyInput{}, mapNotFound("load dependency signal", err)
		}
		if taskMission != signalMission {
			return AddTaskSignalDependencyInput{}, ErrSignalMissionMismatch
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_signal_dependencies(task_id, signal_id, created_at)
			VALUES (?, ?, ?)`, in.TaskID, in.SignalID, formatTime(time.Now())); err != nil {
			return AddTaskSignalDependencyInput{}, fmt.Errorf("insert task signal dependency: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &taskMission, TaskID: &in.TaskID, Actor: in.Actor,
			Type: "task.signal_dependency_added", CommandID: &commandID,
			Payload: map[string]any{"signal_id": in.SignalID},
		}); err != nil {
			return AddTaskSignalDependencyInput{}, err
		}
		return in, nil
	})
}

type ListSignalsFilter struct {
	MissionID domain.MissionID
	Status    signalpolicy.SignalStatus
}

func (s *Store) Signals(ctx context.Context, filter ListSignalsFilter) ([]signalpolicy.Signal, error) {
	query := signalSelect + " WHERE 1 = 1"
	var args []any
	if filter.MissionID != "" {
		query += " AND mission_id = ?"
		args = append(args, filter.MissionID)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	query += " ORDER BY created_at, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list signals: %w", err)
	}
	defer rows.Close()
	result := make([]signalpolicy.Signal, 0)
	for rows.Next() {
		signal, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, signal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate signals: %w", err)
	}
	return result, nil
}

func (s *Store) Signal(ctx context.Context, signalID signalpolicy.SignalID) (signalpolicy.Signal, error) {
	return scanSignal(s.db.QueryRowContext(ctx, signalSelect+" WHERE id = ?", signalID))
}

const signalSelect = `SELECT id, mission_id, task_id, kind, question, context, options_json,
	recommendation, status, answer, version, created_at, resolved_at FROM signals`

func getSignalTx(ctx context.Context, tx *sql.Tx, signalID signalpolicy.SignalID) (signalpolicy.Signal, error) {
	return scanSignal(tx.QueryRowContext(ctx, signalSelect+" WHERE id = ?", signalID))
}

func scanSignal(row rowScanner) (signalpolicy.Signal, error) {
	var signal signalpolicy.Signal
	var taskID, answer, created, resolved sql.NullString
	var optionsJSON []byte
	if err := row.Scan(&signal.ID, &signal.MissionID, &taskID, &signal.Kind, &signal.Question,
		&signal.Context, &optionsJSON, &signal.Recommendation, &signal.Status, &answer,
		&signal.Version, &created, &resolved); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return signalpolicy.Signal{}, ErrNotFound
		}
		return signalpolicy.Signal{}, fmt.Errorf("scan signal: %w", err)
	}
	if taskID.Valid {
		value := domain.TaskID(taskID.String)
		signal.TaskID = &value
	}
	if answer.Valid {
		signal.Answer = &answer.String
	}
	if err := json.Unmarshal(optionsJSON, &signal.Options); err != nil {
		return signalpolicy.Signal{}, fmt.Errorf("decode signal options: %w", err)
	}
	var err error
	signal.CreatedAt, err = parseTime(created.String)
	if err == nil && resolved.Valid {
		signal.ResolvedAt, err = parseOptionalTime(&resolved.String)
	}
	if err != nil {
		return signalpolicy.Signal{}, fmt.Errorf("parse signal timestamp: %w", err)
	}
	return signal, nil
}

func verifyTaskMission(ctx context.Context, tx *sql.Tx, taskID domain.TaskID, missionID domain.MissionID) error {
	var actual domain.MissionID
	if err := tx.QueryRowContext(ctx, "SELECT mission_id FROM tasks WHERE id = ?", taskID).Scan(&actual); err != nil {
		return mapNotFound("load signal task", err)
	}
	if actual != missionID {
		return ErrSignalMissionMismatch
	}
	return nil
}

func openSignalDependenciesTx(ctx context.Context, tx *sql.Tx, taskID domain.TaskID) ([]signalpolicy.SignalID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.signal_id
		FROM task_signal_dependencies d
		JOIN signals s ON s.id = d.signal_id
		WHERE d.task_id = ? AND s.status = ?
		ORDER BY d.signal_id`, taskID, signalpolicy.SignalOpen)
	if err != nil {
		return nil, fmt.Errorf("load signal dependencies: %w", err)
	}
	defer rows.Close()
	var signalIDs []signalpolicy.SignalID
	for rows.Next() {
		var signalID signalpolicy.SignalID
		if err := rows.Scan(&signalID); err != nil {
			return nil, fmt.Errorf("scan signal dependency: %w", err)
		}
		signalIDs = append(signalIDs, signalID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate signal dependencies: %w", err)
	}
	return signalIDs, nil
}

func mapNotFound(action string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", action, err)
}
