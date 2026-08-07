package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"parallel-intellect/internal/delivery"
	"parallel-intellect/internal/domain"
	taskpolicy "parallel-intellect/internal/task"
)

const deliverySelect = `SELECT task_id, attempt, mode, repository, branch, head_sha,
	pr_url, pr_number, state, gate_state, gate_output, command_id, request_base, request_actor,
	release_command_id, release_state, release_actor, created_at, updated_at, delivered_at
	FROM deliveries WHERE task_id = ? AND attempt = ?`

func (s *Store) ReserveDelivery(ctx context.Context, commandID domain.CommandID, in delivery.ReserveInput) (delivery.Reservation, error) {
	if in.TaskID == "" || strings.TrimSpace(in.Operation) == "" || strings.TrimSpace(in.Actor) == "" {
		return delivery.Reservation{}, errors.New("delivery task, operation, and actor are required")
	}
	return runCommand(ctx, s, commandID, "delivery.request", in, func(tx *sql.Tx) (delivery.Reservation, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Reservation{}, err
		}
		return delivery.Reservation{TaskID: current.ID, Attempt: current.CurrentAttempt, Base: in.Base}, nil
	})
}

func (s *Store) PrepareDeliveryRelease(ctx context.Context, commandID domain.CommandID, in delivery.ReleaseIntentInput) error {
	_, err := runCommand(ctx, s, commandID, "delivery.release.prepare", in, func(tx *sql.Tx) (struct{}, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return struct{}{}, err
		}
		if current.CurrentAttempt != in.Attempt || current.State != domain.TaskDeliveredBranch {
			return struct{}{}, errors.New("release intent no longer targets a delivered branch attempt")
		}
		var leaseID, holder string
		var leaseState domain.TreehouseLeaseState
		if err := tx.QueryRowContext(ctx, `SELECT lease_id, lease_holder, state FROM treehouse_leases
			WHERE task_id = ? AND attempt = ?`, in.TaskID, in.Attempt).Scan(&leaseID, &holder, &leaseState); err != nil {
			return struct{}{}, err
		}
		if leaseID != in.LeaseID || holder != in.LeaseHolder ||
			(leaseState != domain.TreehouseLeaseActive && leaseState != domain.TreehouseLeaseReleased) {
			return struct{}{}, ErrLeaseConflict
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE deliveries SET release_command_id = ?, release_state = 'pending', release_actor = ?,
			updated_at = ? WHERE task_id = ? AND attempt = ? AND state = ? AND release_state IN ('', 'pending')`,
			in.RequestCommandID, in.Actor, formatTime(now), in.TaskID, in.Attempt, delivery.StateDeliveredBranch)
		if err != nil {
			return struct{}{}, fmt.Errorf("prepare delivery release: %w", err)
		}
		if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
			return struct{}{}, errors.New("delivery release intent conflicts with existing state")
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: "delivery.release_started", CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "lease_id": in.LeaseID,
				"lease_holder": in.LeaseHolder}}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Store) CompleteDeliveryRelease(ctx context.Context, commandID domain.CommandID, in delivery.ReleaseIntentInput) error {
	_, err := runCommand(ctx, s, commandID, "delivery.release.complete", in, func(tx *sql.Tx) (struct{}, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return struct{}{}, err
		}
		var leaseState domain.TreehouseLeaseState
		if err := tx.QueryRowContext(ctx, `SELECT state FROM treehouse_leases WHERE task_id = ? AND attempt = ?
			AND lease_id = ? AND lease_holder = ?`, in.TaskID, in.Attempt, in.LeaseID, in.LeaseHolder).Scan(&leaseState); err != nil {
			return struct{}{}, err
		}
		if leaseState != domain.TreehouseLeaseReleased {
			return struct{}{}, ErrLeaseConflict
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE deliveries SET release_state = 'completed', updated_at = ?
			WHERE task_id = ? AND attempt = ? AND release_command_id = ? AND release_state = 'pending'`,
			formatTime(now), in.TaskID, in.Attempt, in.RequestCommandID)
		if err != nil {
			return struct{}{}, fmt.Errorf("complete delivery release: %w", err)
		}
		if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
			return struct{}{}, errors.New("delivery release completion conflicts with existing state")
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: "delivery.release_completed", CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "lease_id": in.LeaseID,
				"lease_holder": in.LeaseHolder}}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

// PendingDeliveryRelease is used by Treehouse startup reconciliation before it
// classifies a missing external lease as an unexpected mismatch.
func (s *Store) PendingDeliveryRelease(ctx context.Context, taskID domain.TaskID, attempt int,
	leaseID, leaseHolder string) (*delivery.ReleaseIntentInput, error) {
	var commandID domain.CommandID
	var actor string
	err := s.db.QueryRowContext(ctx, `SELECT d.release_command_id, d.release_actor FROM deliveries d
		JOIN treehouse_leases l ON l.task_id = d.task_id AND l.attempt = d.attempt
		WHERE d.task_id = ? AND d.attempt = ? AND d.release_state = 'pending'
		AND l.lease_id = ? AND l.lease_holder = ? AND l.state = 'active'`,
		taskID, attempt, leaseID, leaseHolder).Scan(&commandID, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect pending delivery release: %w", err)
	}
	return &delivery.ReleaseIntentInput{TaskID: taskID, Attempt: attempt, LeaseID: leaseID,
		LeaseHolder: leaseHolder, RequestCommandID: commandID, Actor: actor}, nil
}

func (s *Store) DeliveryTarget(ctx context.Context, taskID domain.TaskID, attempt int) (delivery.Target, error) {
	if taskID == "" || attempt < 1 {
		return delivery.Target{}, errors.New("delivery task and attempt are required")
	}
	task, err := s.Task(ctx, taskID)
	if err != nil {
		return delivery.Target{}, err
	}
	if task.CurrentAttempt != attempt {
		return delivery.Target{}, ErrStaleAttempt
	}
	attemptRecord, err := s.Attempt(ctx, taskID, attempt)
	if err != nil {
		return delivery.Target{}, err
	}
	var projectPath string
	if err := s.db.QueryRowContext(ctx, `SELECT p.path FROM tasks t
		JOIN missions m ON m.id = t.mission_id JOIN projects p ON p.id = m.project_id
		WHERE t.id = ?`, taskID).Scan(&projectPath); err != nil {
		return delivery.Target{}, fmt.Errorf("load delivery project: %w", err)
	}
	return delivery.Target{Task: task, Attempt: attemptRecord, ProjectPath: projectPath}, nil
}

func (s *Store) Delivery(ctx context.Context, taskID domain.TaskID, attempt int) (*delivery.Record, error) {
	record, err := scanDelivery(s.db.QueryRowContext(ctx, deliverySelect, taskID, attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Store) PrepareDelivery(ctx context.Context, commandID domain.CommandID, in delivery.PrepareInput) (delivery.Result, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.Mode == "" ||
		strings.TrimSpace(in.Branch) == "" || strings.TrimSpace(in.HeadSHA) == "" || in.RequestCommandID == "" ||
		strings.TrimSpace(in.Actor) == "" {
		return delivery.Result{}, errors.New("complete delivery preparation is required")
	}
	if in.Mode != domain.DeliveryBranch && strings.TrimSpace(in.Repository) == "" {
		return delivery.Result{}, errors.New("remote delivery repository is required")
	}
	return runCommand(ctx, s, commandID, "delivery.prepare", in, func(tx *sql.Tx) (delivery.Result, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Result{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return delivery.Result{}, ErrStaleAttempt
		}
		if current.DeliveryMode != in.Mode {
			return delivery.Result{}, errors.New("delivery mode changed")
		}
		var branch, head string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(branch, ''), COALESCE(head_sha, '')
			FROM task_attempts WHERE task_id = ? AND attempt = ?`, in.TaskID, in.Attempt).Scan(&branch, &head); err != nil {
			return delivery.Result{}, fmt.Errorf("load delivery attempt: %w", err)
		}
		if branch != in.Branch {
			return delivery.Result{}, delivery.ErrBranchMismatch
		}
		if !strings.EqualFold(head, in.HeadSHA) {
			return delivery.Result{}, delivery.ErrHeadMismatch
		}

		existing, existingErr := scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
		if existingErr == nil {
			if existing.Mode != in.Mode || existing.Repository != in.Repository || existing.Branch != in.Branch ||
				!strings.EqualFold(existing.HeadSHA, in.HeadSHA) {
				return delivery.Result{}, errors.New("delivery intent conflicts with the existing attempt delivery")
			}
			if existing.State != delivery.StateBlocked || current.State != domain.TaskDeliveryBlocked {
				return delivery.Result{Task: current, Delivery: existing}, nil
			}
			if err := taskpolicy.ValidateTransition(current, domain.TaskValidating); err != nil {
				return delivery.Result{}, err
			}
			now := time.Now().UTC()
			result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskValidating,
				formatTime(now), current.ID, domain.TaskDeliveryBlocked, current.Version, in.Attempt)
			if err != nil {
				return delivery.Result{}, fmt.Errorf("resume delivery: %w", err)
			}
			if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
				return delivery.Result{}, &ConflictError{Current: current}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state = ?, gate_state = ?, gate_output = NULL,
				command_id = ?, updated_at = ? WHERE task_id = ? AND attempt = ? AND state = ?`, delivery.StatePending,
				delivery.GatePending, in.RequestCommandID, formatTime(now), in.TaskID, in.Attempt, delivery.StateBlocked); err != nil {
				return delivery.Result{}, fmt.Errorf("resume delivery record: %w", err)
			}
			updated, err := getTaskTx(ctx, tx, in.TaskID)
			if err != nil {
				return delivery.Result{}, err
			}
			existing, err = scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
			if err != nil {
				return delivery.Result{}, err
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
				Actor: in.Actor, Type: "task.validating", CommandID: &commandID,
				Payload: map[string]any{"from": domain.TaskDeliveryBlocked, "to": domain.TaskValidating,
					"attempt": in.Attempt, "version": updated.Version}}); err != nil {
				return delivery.Result{}, err
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
				Actor: in.Actor, Type: "delivery.resumed", CommandID: &commandID,
				Payload: map[string]any{"attempt": in.Attempt, "head_sha": in.HeadSHA}}); err != nil {
				return delivery.Result{}, err
			}
			return delivery.Result{Task: updated, Delivery: existing}, nil
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return delivery.Result{}, existingErr
		}
		if current.State != domain.TaskReady || current.Version != in.ExpectedVersion {
			return delivery.Result{}, &ConflictError{Current: current}
		}

		now := time.Now().UTC()
		to := domain.TaskValidating
		state := delivery.StatePending
		gateState := delivery.GateNotRequired
		var deliveredAt any
		if in.Mode == domain.DeliveryBranch {
			to = domain.TaskDeliveredBranch
			state = delivery.StateDeliveredBranch
			deliveredAt = formatTime(now)
		} else if in.Mode == domain.DeliveryGate {
			gateState = delivery.GatePending
		}
		if err := taskpolicy.ValidateTransition(current, to); err != nil {
			return delivery.Result{}, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1,
			updated_at = ?, completed_at = ? WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`,
			to, formatTime(now), deliveredAt, in.TaskID, domain.TaskReady, in.ExpectedVersion, in.Attempt)
		if err != nil {
			return delivery.Result{}, fmt.Errorf("start delivery: %w", err)
		}
		if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
			return delivery.Result{}, &ConflictError{Current: current}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries(task_id, attempt, mode, repository,
			branch, head_sha, state, gate_state, command_id, request_base, request_actor,
			created_at, updated_at, delivered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, in.TaskID, in.Attempt, in.Mode, in.Repository,
			in.Branch, strings.ToLower(in.HeadSHA), state, gateState, in.RequestCommandID,
			in.RequestBase, in.Actor, formatTime(now), formatTime(now), deliveredAt); err != nil {
			return delivery.Result{}, fmt.Errorf("insert delivery: %w", err)
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Result{}, err
		}
		record, err := scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
		if err != nil {
			return delivery.Result{}, err
		}
		taskEventType := "task.validating"
		eventType := "delivery.started"
		if in.Mode == domain.DeliveryBranch {
			taskEventType = "task.delivered_branch"
			eventType = "delivery.completed"
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
			Actor: in.Actor, Type: taskEventType, CommandID: &commandID,
			Payload: map[string]any{"from": domain.TaskReady, "to": to, "attempt": in.Attempt,
				"version": updated.Version, "head_sha": strings.ToLower(in.HeadSHA)}}); err != nil {
			return delivery.Result{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
			Actor: in.Actor, Type: eventType, CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "mode": in.Mode, "branch": in.Branch,
				"head_sha": strings.ToLower(in.HeadSHA), "state": state}}); err != nil {
			return delivery.Result{}, err
		}
		if in.Mode == domain.DeliveryBranch {
			if _, err := regenerateMissionDigestTx(ctx, tx, updated.MissionID, "control-plane", "task.delivered_branch", &commandID); err != nil {
				return delivery.Result{}, err
			}
		}
		return delivery.Result{Task: updated, Delivery: record}, nil
	})
}

func (s *Store) RecordDeliveryGate(ctx context.Context, commandID domain.CommandID, in delivery.GateInput) (delivery.Result, error) {
	if in.TaskID == "" || in.Attempt < 1 || strings.TrimSpace(in.HeadSHA) == "" || strings.TrimSpace(in.Actor) == "" {
		return delivery.Result{}, errors.New("complete delivery gate result is required")
	}
	if len(in.Output) > 1<<20 {
		return delivery.Result{}, errors.New("delivery gate output exceeds 1 MiB")
	}
	return runCommand(ctx, s, commandID, "delivery.gate", in, func(tx *sql.Tx) (delivery.Result, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Result{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return delivery.Result{}, ErrStaleAttempt
		}
		record, err := scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
		if err != nil {
			return delivery.Result{}, err
		}
		if record.Mode != domain.DeliveryGate || record.State != delivery.StatePending ||
			current.State != domain.TaskValidating || !strings.EqualFold(record.HeadSHA, in.HeadSHA) {
			return delivery.Result{}, errors.New("delivery gate no longer matches a pending validating attempt")
		}
		now := time.Now().UTC()
		gateState := delivery.GatePassed
		state := delivery.StatePending
		eventType := "delivery.gate_passed"
		if !in.Passed {
			gateState = delivery.GateFailed
			state = delivery.StateBlocked
			eventType = "delivery.gate_failed"
			if err := taskpolicy.ValidateTransition(current, domain.TaskDeliveryBlocked); err != nil {
				return delivery.Result{}, err
			}
			result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1,
				updated_at = ? WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`,
				domain.TaskDeliveryBlocked, formatTime(now), current.ID, domain.TaskValidating,
				current.Version, in.Attempt)
			if err != nil {
				return delivery.Result{}, fmt.Errorf("block failed delivery gate: %w", err)
			}
			if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
				return delivery.Result{}, &ConflictError{Current: current}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state = ?, gate_state = ?, gate_output = ?,
			updated_at = ? WHERE task_id = ? AND attempt = ? AND state = ?`, state, gateState,
			in.Output, formatTime(now), in.TaskID, in.Attempt, delivery.StatePending); err != nil {
			return delivery.Result{}, fmt.Errorf("record delivery gate: %w", err)
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Result{}, err
		}
		record, err = scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
		if err != nil {
			return delivery.Result{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
			Actor: in.Actor, Type: eventType, CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "head_sha": record.HeadSHA, "state": state}}); err != nil {
			return delivery.Result{}, err
		}
		if !in.Passed {
			if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
				Actor: in.Actor, Type: "task.delivery_blocked", CommandID: &commandID,
				Payload: map[string]any{"from": domain.TaskValidating, "to": domain.TaskDeliveryBlocked,
					"attempt": in.Attempt, "version": updated.Version}}); err != nil {
				return delivery.Result{}, err
			}
		}
		return delivery.Result{Task: updated, Delivery: record}, nil
	})
}

func (s *Store) CompleteDelivery(ctx context.Context, commandID domain.CommandID, in delivery.CompleteInput) (delivery.Result, error) {
	if in.TaskID == "" || in.Attempt < 1 || strings.TrimSpace(in.Repository) == "" ||
		strings.TrimSpace(in.Branch) == "" || strings.TrimSpace(in.HeadSHA) == "" ||
		strings.TrimSpace(in.PRURL) == "" || in.PRNumber < 1 || strings.TrimSpace(in.Actor) == "" {
		return delivery.Result{}, errors.New("complete pull request delivery is required")
	}
	return runCommand(ctx, s, commandID, "delivery.complete", in, func(tx *sql.Tx) (delivery.Result, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Result{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return delivery.Result{}, ErrStaleAttempt
		}
		record, err := scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
		if err != nil {
			return delivery.Result{}, err
		}
		if current.State != domain.TaskValidating || record.State != delivery.StatePending ||
			record.Repository != in.Repository || record.Branch != in.Branch || !strings.EqualFold(record.HeadSHA, in.HeadSHA) {
			return delivery.Result{}, errors.New("pull request does not match the pending delivery intent")
		}
		if record.Mode == domain.DeliveryGate && record.GateState != delivery.GatePassed {
			return delivery.Result{}, errors.New("no-mistakes gate has not passed")
		}
		if record.Mode == domain.DeliveryPR && record.GateState != delivery.GateNotRequired {
			return delivery.Result{}, errors.New("direct PR delivery has an invalid gate state")
		}
		if err := taskpolicy.ValidateTransition(current, domain.TaskDelivered); err != nil {
			return delivery.Result{}, err
		}
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1,
			updated_at = ?, completed_at = ? WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`,
			domain.TaskDelivered, formatTime(now), formatTime(now), current.ID, domain.TaskValidating,
			current.Version, in.Attempt)
		if err != nil {
			return delivery.Result{}, fmt.Errorf("complete delivered task: %w", err)
		}
		if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
			return delivery.Result{}, &ConflictError{Current: current}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state = ?, pr_url = ?, pr_number = ?,
			updated_at = ?, delivered_at = ? WHERE task_id = ? AND attempt = ? AND state = ?`,
			delivery.StateDelivered, in.PRURL, in.PRNumber, formatTime(now), formatTime(now),
			in.TaskID, in.Attempt, delivery.StatePending); err != nil {
			return delivery.Result{}, fmt.Errorf("complete delivery record: %w", err)
		}
		updated, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return delivery.Result{}, err
		}
		record, err = scanDelivery(tx.QueryRowContext(ctx, deliverySelect, in.TaskID, in.Attempt))
		if err != nil {
			return delivery.Result{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
			Actor: in.Actor, Type: "task.delivered", CommandID: &commandID,
			Payload: map[string]any{"from": domain.TaskValidating, "to": domain.TaskDelivered,
				"attempt": in.Attempt, "version": updated.Version, "head_sha": record.HeadSHA}}); err != nil {
			return delivery.Result{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &updated.MissionID, TaskID: &updated.ID,
			Actor: in.Actor, Type: "delivery.completed", CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "mode": record.Mode, "branch": record.Branch,
				"head_sha": record.HeadSHA, "pr_url": in.PRURL, "pr_number": in.PRNumber}}); err != nil {
			return delivery.Result{}, err
		}
		if _, err := regenerateMissionDigestTx(ctx, tx, updated.MissionID, "control-plane", "task.delivered", &commandID); err != nil {
			return delivery.Result{}, err
		}
		return delivery.Result{Task: updated, Delivery: record}, nil
	})
}

func scanDelivery(row rowScanner) (delivery.Record, error) {
	var record delivery.Record
	var prURL, gateOutput, releaseCommand, created, updated, deliveredAt sql.NullString
	var prNumber sql.NullInt64
	if err := row.Scan(&record.TaskID, &record.Attempt, &record.Mode, &record.Repository,
		&record.Branch, &record.HeadSHA, &prURL, &prNumber, &record.State, &record.GateState,
		&gateOutput, &record.CommandID, &record.RequestBase, &record.RequestActor,
		&releaseCommand, &record.ReleaseState, &record.ReleaseActor,
		&created, &updated, &deliveredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return delivery.Record{}, sql.ErrNoRows
		}
		return delivery.Record{}, fmt.Errorf("scan delivery: %w", err)
	}
	record.PRURL = prURL.String
	record.PRNumber = int(prNumber.Int64)
	record.GateOutput = gateOutput.String
	record.ReleaseCommandID = domain.CommandID(releaseCommand.String)
	var err error
	record.CreatedAt, err = parseTime(created.String)
	if err == nil {
		record.UpdatedAt, err = parseTime(updated.String)
	}
	if err == nil && deliveredAt.Valid {
		record.DeliveredAt, err = parseOptionalTime(&deliveredAt.String)
	}
	if err != nil {
		return delivery.Record{}, fmt.Errorf("parse delivery timestamp: %w", err)
	}
	return record, nil
}
