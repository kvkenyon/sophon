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

type LeaseAcquisitionTarget struct {
	TaskID          domain.TaskID
	TaskTitle       string
	Attempt         int
	ExpectedVersion int64
	Project         string
	ProjectPath     string
	Existing        *domain.TreehouseLease
}

// UnleasedProvisioningTargets identifies attempts that may have crashed after
// Treehouse allocation but before the lease transaction committed. The
// external reconciler may adopt only an exact deterministic holder match.
func (s *Store) UnleasedProvisioningTargets(ctx context.Context) ([]LeaseAcquisitionTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.title, t.current_attempt, t.version, p.name, p.path
		FROM tasks t
		JOIN missions m ON m.id = t.mission_id
		JOIN projects p ON p.id = m.project_id
		LEFT JOIN treehouse_leases l ON l.task_id = t.id AND l.attempt = t.current_attempt
		WHERE t.state = ? AND l.lease_id IS NULL
		ORDER BY t.created_at, t.id`, domain.TaskProvisioning)
	if err != nil {
		return nil, fmt.Errorf("list unleased provisioning tasks: %w", err)
	}
	defer rows.Close()
	var targets []LeaseAcquisitionTarget
	for rows.Next() {
		var target LeaseAcquisitionTarget
		if err := rows.Scan(&target.TaskID, &target.TaskTitle, &target.Attempt, &target.ExpectedVersion,
			&target.Project, &target.ProjectPath); err != nil {
			return nil, fmt.Errorf("scan unleased provisioning task: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unleased provisioning tasks: %w", err)
	}
	return targets, nil
}

// LeaseTarget verifies that attempt is still authoritative and returns the
// registered project used as the working directory for Treehouse.
func (s *Store) LeaseTarget(ctx context.Context, taskID domain.TaskID, attempt int) (LeaseAcquisitionTarget, error) {
	if taskID == "" || attempt < 1 {
		return LeaseAcquisitionTarget{}, errors.New("task and attempt are required")
	}
	var target LeaseAcquisitionTarget
	var state domain.TaskState
	err := s.db.QueryRowContext(ctx, `SELECT t.id, t.title, t.current_attempt, t.version, t.state, p.name, p.path
		FROM tasks t
		JOIN missions m ON m.id = t.mission_id
		JOIN projects p ON p.id = m.project_id
		WHERE t.id = ?`, taskID).Scan(&target.TaskID, &target.TaskTitle, &target.Attempt, &target.ExpectedVersion, &state,
		&target.Project, &target.ProjectPath)
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseAcquisitionTarget{}, ErrNotFound
	}
	if err != nil {
		return LeaseAcquisitionTarget{}, fmt.Errorf("load lease target: %w", err)
	}
	if target.Attempt != attempt {
		return LeaseAcquisitionTarget{}, ErrStaleAttempt
	}
	lease, err := s.TreehouseLease(ctx, taskID, attempt)
	if err == nil {
		target.Existing = &lease
	} else if !errors.Is(err, ErrNotFound) {
		return LeaseAcquisitionTarget{}, err
	}
	if target.Existing == nil && state != domain.TaskProvisioning {
		return LeaseAcquisitionTarget{}, fmt.Errorf("acquire Treehouse lease while task is %s: %w", state, ErrLeaseConflict)
	}
	return target, nil
}

type RecordTreehouseLeaseInput struct {
	TaskID          domain.TaskID         `json:"task_id"`
	Attempt         int                   `json:"attempt"`
	ExpectedVersion int64                 `json:"expected_version"`
	Lease           domain.TreehouseLease `json:"lease"`
	Actor           string                `json:"actor"`
}

// RecordTreehouseLease atomically binds an acquired external lease to its
// current attempt, updates the attempt projection, and emits its audit event.
func (s *Store) RecordTreehouseLease(ctx context.Context, commandID domain.CommandID, in RecordTreehouseLeaseInput) (domain.TreehouseLease, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || in.Actor == "" ||
		in.Lease.LeaseID == "" || in.Lease.LeaseHolder == "" || in.Lease.WorktreePath == "" ||
		in.Lease.Project == "" || in.Lease.Branch == "" || in.Lease.BaseSHA == "" {
		return domain.TreehouseLease{}, errors.New("complete Treehouse lease identity is required")
	}
	return runCommand(ctx, s, commandID, "treehouse.lease.record", in, func(tx *sql.Tx) (domain.TreehouseLease, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.TreehouseLease{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return domain.TreehouseLease{}, ErrStaleAttempt
		}
		if current.Version != in.ExpectedVersion || current.State != domain.TaskProvisioning {
			return domain.TreehouseLease{}, &ConflictError{Current: current}
		}
		var leaseCount int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM treehouse_leases WHERE task_id = ? AND attempt = ?",
			in.TaskID, in.Attempt).Scan(&leaseCount); err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("inspect existing lease: %w", err)
		}
		if leaseCount != 0 {
			return domain.TreehouseLease{}, ErrLeaseExists
		}

		now := time.Now().UTC()
		lease := in.Lease
		lease.TaskID = in.TaskID
		lease.Attempt = in.Attempt
		lease.State = domain.TreehouseLeaseActive
		if lease.AcquiredAt.IsZero() {
			lease.AcquiredAt = now
		} else {
			lease.AcquiredAt = lease.AcquiredAt.UTC()
		}
		lease.ReleasedAt = nil
		if _, err := tx.ExecContext(ctx, `INSERT INTO treehouse_leases(
			lease_id, task_id, attempt, lease_holder, worktree_path, project, branch, base_sha,
			state, acquired_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, lease.LeaseID, lease.TaskID, lease.Attempt,
			lease.LeaseHolder, lease.WorktreePath, lease.Project, lease.Branch, lease.BaseSHA,
			lease.State, formatTime(lease.AcquiredAt)); err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("insert Treehouse lease: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE task_attempts SET
			base_sha = ?, branch = ?, worktree_path = ?, treehouse_lease_id = ?, treehouse_lease_holder = ?
			WHERE task_id = ? AND attempt = ?
			AND treehouse_lease_id IS NULL AND treehouse_lease_holder IS NULL AND worktree_path IS NULL`,
			lease.BaseSHA, lease.Branch, lease.WorktreePath, lease.LeaseID, lease.LeaseHolder,
			in.TaskID, in.Attempt)
		if err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("bind lease to attempt: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return domain.TreehouseLease{}, fmt.Errorf("count attempt lease binding: %w", err)
			}
			return domain.TreehouseLease{}, ErrLeaseExists
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks
			SET base_sha = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND current_attempt = ? AND version = ? AND state = ?`,
			lease.BaseSHA, formatTime(now), in.TaskID, in.Attempt, in.ExpectedVersion, domain.TaskProvisioning)
		if err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("update task lease projection: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return domain.TreehouseLease{}, fmt.Errorf("count task lease projection: %w", err)
			}
			reloaded, reloadErr := getTaskTx(ctx, tx, in.TaskID)
			if reloadErr != nil {
				return domain.TreehouseLease{}, reloadErr
			}
			return domain.TreehouseLease{}, &ConflictError{Current: reloaded}
		}
		if err := appendEvent(ctx, tx, eventInput{
			MissionID: &current.MissionID, TaskID: &in.TaskID, Actor: in.Actor,
			Type: "treehouse.lease_acquired", CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "lease_id": lease.LeaseID,
				"lease_holder": lease.LeaseHolder, "worktree_path": lease.WorktreePath,
				"base_sha": lease.BaseSHA, "branch": lease.Branch},
		}); err != nil {
			return domain.TreehouseLease{}, err
		}
		return lease, nil
	})
}

func (s *Store) TreehouseLease(ctx context.Context, taskID domain.TaskID, attempt int) (domain.TreehouseLease, error) {
	return scanTreehouseLease(s.db.QueryRowContext(ctx, `SELECT l.lease_id, l.task_id, l.attempt,
		l.lease_holder, l.worktree_path, l.project, p.path, l.branch, l.base_sha, l.state,
		l.acquired_at, l.released_at
		FROM treehouse_leases l
		JOIN tasks t ON t.id = l.task_id
		JOIN missions m ON m.id = t.mission_id
		JOIN projects p ON p.id = m.project_id
		WHERE l.task_id = ? AND l.attempt = ?`, taskID, attempt))
}

func (s *Store) ActiveTreehouseLeases(ctx context.Context) ([]domain.TreehouseLease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.lease_id, l.task_id, l.attempt,
		l.lease_holder, l.worktree_path, l.project, p.path, l.branch, l.base_sha, l.state,
		l.acquired_at, l.released_at
		FROM treehouse_leases l
		JOIN tasks t ON t.id = l.task_id
		JOIN missions m ON m.id = t.mission_id
		JOIN projects p ON p.id = m.project_id
		WHERE l.state = 'active' ORDER BY l.acquired_at, l.lease_id`)
	if err != nil {
		return nil, fmt.Errorf("query active Treehouse leases: %w", err)
	}
	defer rows.Close()
	var leases []domain.TreehouseLease
	for rows.Next() {
		lease, err := scanTreehouseLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active Treehouse leases: %w", err)
	}
	return leases, nil
}

type ReleaseTreehouseLeaseInput struct {
	TaskID      domain.TaskID `json:"task_id"`
	Attempt     int           `json:"attempt"`
	LeaseID     string        `json:"lease_id"`
	LeaseHolder string        `json:"lease_holder"`
	Actor       string        `json:"actor"`
}

func (s *Store) MarkTreehouseLeaseReleased(ctx context.Context, commandID domain.CommandID, in ReleaseTreehouseLeaseInput) (domain.TreehouseLease, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.LeaseID == "" || in.LeaseHolder == "" || in.Actor == "" {
		return domain.TreehouseLease{}, errors.New("task, attempt, lease identity, and actor are required")
	}
	return runCommand(ctx, s, commandID, "treehouse.lease.release", in, func(tx *sql.Tx) (domain.TreehouseLease, error) {
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE treehouse_leases
			SET state = ?, released_at = ?
			WHERE task_id = ? AND attempt = ? AND lease_id = ? AND lease_holder = ? AND state = ?`,
			domain.TreehouseLeaseReleased, formatTime(now), in.TaskID, in.Attempt,
			in.LeaseID, in.LeaseHolder, domain.TreehouseLeaseActive)
		if err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("mark Treehouse lease released: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return domain.TreehouseLease{}, fmt.Errorf("count released Treehouse lease: %w", err)
			}
			return domain.TreehouseLease{}, ErrLeaseConflict
		}
		lease, err := scanTreehouseLease(tx.QueryRowContext(ctx, leaseSelectByID, in.LeaseID))
		if err != nil {
			return domain.TreehouseLease{}, err
		}
		lease.ReleasedAt = &now
		var missionID domain.MissionID
		if err := tx.QueryRowContext(ctx, "SELECT mission_id FROM tasks WHERE id = ?", in.TaskID).Scan(&missionID); err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("load lease mission: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, TaskID: &in.TaskID,
			Actor: in.Actor, Type: "treehouse.lease_released", CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "lease_id": in.LeaseID,
				"lease_holder": in.LeaseHolder}}); err != nil {
			return domain.TreehouseLease{}, err
		}
		return lease, nil
	})
}

// FenceTreehouseLeaseAfterReleaseFailure records an ambiguous conditional
// return. The lease is never released by path alone and the task lifecycle is
// intentionally left alone: cancellation is already terminal.
func (s *Store) FenceTreehouseLeaseAfterReleaseFailure(ctx context.Context, commandID domain.CommandID, in ReleaseTreehouseLeaseInput, reason string) (domain.TreehouseLease, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.LeaseID == "" || in.LeaseHolder == "" || in.Actor == "" {
		return domain.TreehouseLease{}, errors.New("task, attempt, lease identity, and actor are required")
	}
	return runCommand(ctx, s, commandID, "treehouse.lease.release_failure", struct {
		ReleaseTreehouseLeaseInput
		Reason string `json:"reason"`
	}{in, reason}, func(tx *sql.Tx) (domain.TreehouseLease, error) {
		result, err := tx.ExecContext(ctx, `UPDATE treehouse_leases SET state = ? WHERE task_id = ? AND attempt = ? AND lease_id = ? AND lease_holder = ? AND state = ?`,
			domain.TreehouseLeaseFenced, in.TaskID, in.Attempt, in.LeaseID, in.LeaseHolder, domain.TreehouseLeaseActive)
		if err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("fence failed release: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return domain.TreehouseLease{}, ErrLeaseConflict
		}
		lease, err := scanTreehouseLease(tx.QueryRowContext(ctx, leaseSelectByID, in.LeaseID))
		if err != nil {
			return domain.TreehouseLease{}, err
		}
		var missionID domain.MissionID
		if err := tx.QueryRowContext(ctx, "SELECT mission_id FROM tasks WHERE id = ?", in.TaskID).Scan(&missionID); err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("load lease mission: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, TaskID: &in.TaskID, Actor: in.Actor, Type: "treehouse.lease_release_failed", CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "lease_id": in.LeaseID, "lease_holder": in.LeaseHolder, "reason": reason}}); err != nil {
			return domain.TreehouseLease{}, err
		}
		return lease, nil
	})
}

type ReconcileTreehouseLeaseInput struct {
	TaskID      domain.TaskID              `json:"task_id"`
	Attempt     int                        `json:"attempt"`
	LeaseID     string                     `json:"lease_id"`
	LeaseHolder string                     `json:"lease_holder"`
	Outcome     domain.TreehouseLeaseState `json:"outcome"`
	Actor       string                     `json:"actor"`
}

// ReconcileTreehouseLease fences a mismatched or missing lease and fails only
// the attempt that is still current. It never performs an external release.
func (s *Store) ReconcileTreehouseLease(ctx context.Context, commandID domain.CommandID, in ReconcileTreehouseLeaseInput) (domain.TreehouseLease, error) {
	if in.Outcome != domain.TreehouseLeaseFenced && in.Outcome != domain.TreehouseLeaseMissing {
		return domain.TreehouseLease{}, errors.New("reconciliation outcome must be fenced or missing")
	}
	if in.TaskID == "" || in.Attempt < 1 || in.LeaseID == "" || in.LeaseHolder == "" || in.Actor == "" {
		return domain.TreehouseLease{}, errors.New("task, attempt, lease identity, and actor are required")
	}
	return runCommand(ctx, s, commandID, "treehouse.lease.reconcile", in, func(tx *sql.Tx) (domain.TreehouseLease, error) {
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE treehouse_leases SET state = ?
			WHERE task_id = ? AND attempt = ? AND lease_id = ? AND lease_holder = ? AND state = ?`,
			in.Outcome, in.TaskID, in.Attempt, in.LeaseID, in.LeaseHolder, domain.TreehouseLeaseActive)
		if err != nil {
			return domain.TreehouseLease{}, fmt.Errorf("fence Treehouse lease: %w", err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return domain.TreehouseLease{}, fmt.Errorf("count fenced Treehouse lease: %w", err)
			}
			return domain.TreehouseLease{}, ErrLeaseConflict
		}
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.TreehouseLease{}, err
		}
		failedCurrent := current.CurrentAttempt == in.Attempt && !taskpolicy.IsTerminal(current.State)
		if failedCurrent {
			result, err = tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1,
				updated_at = ?, completed_at = ? WHERE id = ? AND current_attempt = ? AND version = ?`,
				domain.TaskFailed, formatTime(now), formatTime(now), in.TaskID, in.Attempt, current.Version)
			if err != nil {
				return domain.TreehouseLease{}, fmt.Errorf("fail task after lease reconciliation: %w", err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 1 {
				if err != nil {
					return domain.TreehouseLease{}, fmt.Errorf("count reconciled task failure: %w", err)
				}
				return domain.TreehouseLease{}, &ConflictError{Current: current}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task_attempts SET completed_at = COALESCE(completed_at, ?)
				WHERE task_id = ? AND attempt = ?`, formatTime(now), in.TaskID, in.Attempt); err != nil {
				return domain.TreehouseLease{}, fmt.Errorf("complete reconciled attempt: %w", err)
			}
		}
		eventType := "treehouse.lease_mismatch"
		if in.Outcome == domain.TreehouseLeaseMissing {
			eventType = "treehouse.worktree_missing"
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &in.TaskID,
			Actor: in.Actor, Type: eventType, CommandID: &commandID,
			Payload: map[string]any{"attempt": in.Attempt, "lease_id": in.LeaseID,
				"lease_holder": in.LeaseHolder, "task_failed": failedCurrent}}); err != nil {
			return domain.TreehouseLease{}, err
		}
		return scanTreehouseLease(tx.QueryRowContext(ctx, leaseSelectByID, in.LeaseID))
	})
}

const leaseSelectByID = `SELECT l.lease_id, l.task_id, l.attempt, l.lease_holder,
	l.worktree_path, l.project, p.path, l.branch, l.base_sha, l.state, l.acquired_at, l.released_at
	FROM treehouse_leases l
	JOIN tasks t ON t.id = l.task_id
	JOIN missions m ON m.id = t.mission_id
	JOIN projects p ON p.id = m.project_id
	WHERE l.lease_id = ?`

func scanTreehouseLease(row rowScanner) (domain.TreehouseLease, error) {
	var lease domain.TreehouseLease
	var acquired string
	var released sql.NullString
	if err := row.Scan(&lease.LeaseID, &lease.TaskID, &lease.Attempt, &lease.LeaseHolder,
		&lease.WorktreePath, &lease.Project, &lease.ProjectPath, &lease.Branch, &lease.BaseSHA,
		&lease.State, &acquired, &released); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TreehouseLease{}, ErrNotFound
		}
		return domain.TreehouseLease{}, fmt.Errorf("scan Treehouse lease: %w", err)
	}
	var err error
	lease.AcquiredAt, err = parseTime(acquired)
	if err == nil && released.Valid {
		lease.ReleasedAt, err = parseOptionalTime(&released.String)
	}
	if err != nil {
		return domain.TreehouseLease{}, fmt.Errorf("parse Treehouse lease timestamp: %w", err)
	}
	return lease, nil
}
