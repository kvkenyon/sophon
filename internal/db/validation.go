package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/id"
	taskpolicy "parallel-intellect/internal/task"
	"parallel-intellect/internal/validation"
)

func (s *Store) LookupValidation(ctx context.Context, key validation.Key) (*validation.Record, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	record, err := lookupValidation(ctx, s.db, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup validation cache: %w", err)
	}
	return &record, nil
}

func (s *Store) BeginValidation(ctx context.Context, commandID domain.CommandID, in validation.BeginInput) (domain.Task, error) {
	if in.TaskID == "" || in.Attempt < 1 || strings.TrimSpace(in.Actor) == "" {
		return domain.Task{}, errors.New("task, attempt, and actor are required")
	}
	return runCommand(ctx, s, commandID, "validation.begin", in, func(tx *sql.Tx) (domain.Task, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return domain.Task{}, ErrStaleAttempt
		}
		from := current.State
		switch from {
		case domain.TaskReady, domain.TaskDeliveryBlocked:
			if err := taskpolicy.ValidateTransition(current, domain.TaskValidating); err != nil {
				return domain.Task{}, err
			}
			now := time.Now().UTC()
			result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskValidating,
				formatTime(now), current.ID, from, current.Version, in.Attempt)
			if err != nil {
				return domain.Task{}, fmt.Errorf("begin task validation: %w", err)
			}
			if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
				if rowErr != nil {
					return domain.Task{}, fmt.Errorf("count validation transition: %w", rowErr)
				}
				reloaded, reloadErr := getTaskTx(ctx, tx, current.ID)
				if reloadErr != nil {
					return domain.Task{}, reloadErr
				}
				return domain.Task{}, &ConflictError{Current: reloaded}
			}
			current, err = getTaskTx(ctx, tx, current.ID)
			if err != nil {
				return domain.Task{}, err
			}
			if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
				Actor: in.Actor, Type: "task.validating", CommandID: &commandID, Payload: map[string]any{
					"from": from, "to": domain.TaskValidating, "version": current.Version, "attempt": in.Attempt,
				}}); err != nil {
				return domain.Task{}, err
			}
		case domain.TaskValidating:
			// A prior process may have crashed after recording some cache entries.
			// Starting another validation round in-place lets it resume safely.
		default:
			return domain.Task{}, fmt.Errorf("begin validation while task is %s", from)
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: "validation.started", CommandID: &commandID, Payload: map[string]any{
				"attempt": in.Attempt, "version": current.Version,
			}}); err != nil {
			return domain.Task{}, err
		}
		return current, nil
	})
}

func (s *Store) RecordValidation(ctx context.Context, commandID domain.CommandID, in validation.RecordInput) (validation.Record, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.Key.TaskID != in.TaskID {
		return validation.Record{}, errors.New("validation result task and attempt are required")
	}
	if err := in.Key.Validate(); err != nil {
		return validation.Record{}, err
	}
	if err := in.Result.Validate(); err != nil {
		return validation.Record{}, err
	}
	return runCommand(ctx, s, commandID, "validation.record", in, func(tx *sql.Tx) (validation.Record, error) {
		existing, err := lookupValidation(ctx, tx, in.Key)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return validation.Record{}, fmt.Errorf("inspect validation cache: %w", err)
		}
		var currentAttempt int
		var attemptHead string
		if err := tx.QueryRowContext(ctx, `SELECT t.current_attempt, COALESCE(a.head_sha, '')
			FROM tasks t JOIN task_attempts a ON a.task_id = t.id AND a.attempt = ?
			WHERE t.id = ?`, in.Attempt, in.TaskID).Scan(&currentAttempt, &attemptHead); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return validation.Record{}, ErrNotFound
			}
			return validation.Record{}, fmt.Errorf("verify validation attempt: %w", err)
		}
		if currentAttempt != in.Attempt {
			return validation.Record{}, ErrStaleAttempt
		}
		var taskState domain.TaskState
		if err := tx.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id = ?", in.TaskID).Scan(&taskState); err != nil {
			return validation.Record{}, fmt.Errorf("load validation task state: %w", err)
		}
		if taskState != domain.TaskValidating {
			return validation.Record{}, fmt.Errorf("record validation while task is %s", taskState)
		}
		if !strings.EqualFold(attemptHead, in.Key.HeadSHA) {
			return validation.Record{}, errors.New("validation key head does not match task attempt")
		}

		evidence, err := json.Marshal(in.Result)
		if err != nil {
			return validation.Record{}, fmt.Errorf("encode validation evidence: %w", err)
		}
		evidenceHash := sha256.Sum256(evidence)
		artifactRaw, err := id.New("art")
		if err != nil {
			return validation.Record{}, err
		}
		runID, err := id.New("val")
		if err != nil {
			return validation.Record{}, err
		}
		now := time.Now().UTC()
		artifact := validation.Artifact{
			ID: domain.ArtifactID(artifactRaw), TaskID: in.TaskID, Attempt: in.Attempt,
			Kind: "validation.output", MediaType: "application/json", SHA256: hex.EncodeToString(evidenceHash[:]),
			Content: evidence, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(
			id, task_id, attempt, kind, media_type, sha256, content, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, artifact.TaskID, artifact.Attempt,
			artifact.Kind, artifact.MediaType, artifact.SHA256, artifact.Content, formatTime(now)); err != nil {
			return validation.Record{}, fmt.Errorf("insert validation artifact: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO validation_runs(
			id, task_id, attempt, head_sha, workspace_hash, validator, validator_version,
			config_hash, command_hash, environment_hash, status, artifact_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, in.TaskID, in.Attempt,
			in.Key.HeadSHA, in.Key.WorkspaceHash, in.Key.Validator, in.Key.ValidatorVersion,
			in.Key.ConfigHash, in.Key.CommandHash, in.Key.EnvironmentHash, in.Result.Status,
			artifact.ID, formatTime(now)); err != nil {
			return validation.Record{}, fmt.Errorf("insert validation run: %w", err)
		}
		return validation.Record{
			ID: runID, Attempt: in.Attempt, Key: in.Key, Status: in.Result.Status,
			Result: in.Result, Artifact: artifact, CreatedAt: now,
		}, nil
	})
}

func (s *Store) CompleteValidation(ctx context.Context, commandID domain.CommandID, in validation.CompleteInput) (domain.Task, error) {
	if in.TaskID == "" || in.Attempt < 1 || in.ExpectedVersion < 1 || len(in.RunIDs) == 0 || strings.TrimSpace(in.Actor) == "" ||
		strings.TrimSpace(in.HeadSHA) == "" || strings.TrimSpace(in.WorkspaceHash) == "" ||
		strings.TrimSpace(in.ConfigHash) == "" || strings.TrimSpace(in.EnvironmentHash) == "" {
		return domain.Task{}, errors.New("task, attempt, expected version, validation runs, and actor are required")
	}
	seen := make(map[string]struct{}, len(in.RunIDs))
	for _, runID := range in.RunIDs {
		if runID == "" {
			return domain.Task{}, errors.New("validation run ID is required")
		}
		if _, duplicate := seen[runID]; duplicate {
			return domain.Task{}, errors.New("duplicate validation run ID")
		}
		seen[runID] = struct{}{}
	}
	return runCommand(ctx, s, commandID, "validation.complete", in, func(tx *sql.Tx) (domain.Task, error) {
		current, err := getTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return domain.Task{}, err
		}
		if current.CurrentAttempt != in.Attempt {
			return domain.Task{}, ErrStaleAttempt
		}
		if current.State != domain.TaskValidating || current.Version != in.ExpectedVersion {
			return domain.Task{}, &ConflictError{Current: current}
		}
		failed := false
		artifactIDs := make([]domain.ArtifactID, 0, len(in.RunIDs))
		for _, runID := range in.RunIDs {
			var taskID domain.TaskID
			var status validation.Status
			var artifactID domain.ArtifactID
			var headSHA, workspaceHash, configHash, environmentHash string
			if err := tx.QueryRowContext(ctx, `SELECT task_id, status, artifact_id, head_sha, workspace_hash,
				config_hash, environment_hash FROM validation_runs WHERE id = ?`, runID).Scan(
				&taskID, &status, &artifactID, &headSHA, &workspaceHash, &configHash, &environmentHash); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return domain.Task{}, ErrNotFound
				}
				return domain.Task{}, fmt.Errorf("load validation run: %w", err)
			}
			if taskID != in.TaskID {
				return domain.Task{}, errors.New("validation run belongs to another task")
			}
			if !strings.EqualFold(headSHA, in.HeadSHA) || workspaceHash != in.WorkspaceHash ||
				configHash != in.ConfigHash || environmentHash != in.EnvironmentHash {
				return domain.Task{}, errors.New("validation run input fingerprint does not match completion")
			}
			if status == validation.Failed {
				failed = true
			} else if status != validation.Passed {
				return domain.Task{}, fmt.Errorf("unknown validation status %q", status)
			}
			artifactIDs = append(artifactIDs, artifactID)
		}
		eventType := "validation.passed"
		if failed {
			eventType = "validation.failed"
			if err := taskpolicy.ValidateTransition(current, domain.TaskDeliveryBlocked); err != nil {
				return domain.Task{}, err
			}
			now := time.Now().UTC()
			result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND state = ? AND version = ? AND current_attempt = ?`, domain.TaskDeliveryBlocked,
				formatTime(now), current.ID, domain.TaskValidating, current.Version, in.Attempt)
			if err != nil {
				return domain.Task{}, fmt.Errorf("block failed validation delivery: %w", err)
			}
			if rows, rowErr := result.RowsAffected(); rowErr != nil || rows != 1 {
				if rowErr != nil {
					return domain.Task{}, fmt.Errorf("count failed validation transition: %w", rowErr)
				}
				return domain.Task{}, &ConflictError{Current: current}
			}
			current, err = getTaskTx(ctx, tx, current.ID)
			if err != nil {
				return domain.Task{}, err
			}
		}
		payload := map[string]any{
			"attempt": in.Attempt, "version": current.Version, "run_ids": in.RunIDs,
			"artifact_ids": artifactIDs,
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
			Actor: in.Actor, Type: eventType, CommandID: &commandID, Payload: payload}); err != nil {
			return domain.Task{}, err
		}
		if failed {
			if err := appendEvent(ctx, tx, eventInput{MissionID: &current.MissionID, TaskID: &current.ID,
				Actor: in.Actor, Type: "task.delivery_blocked", CommandID: &commandID, Payload: map[string]any{
					"from": domain.TaskValidating, "to": domain.TaskDeliveryBlocked,
					"version": current.Version, "attempt": in.Attempt,
				}}); err != nil {
				return domain.Task{}, err
			}
		}
		return current, nil
	})
}

type validationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookupValidation(ctx context.Context, queryer validationQueryer, key validation.Key) (validation.Record, error) {
	var record validation.Record
	var artifact validation.Artifact
	var validatorKind string
	var created, artifactCreated string
	var evidence []byte
	err := queryer.QueryRowContext(ctx, `SELECT
		v.id, v.attempt, v.task_id, v.head_sha, v.workspace_hash, v.validator,
		v.validator_version, v.config_hash, v.command_hash, v.environment_hash,
		v.status, v.created_at,
		a.id, a.task_id, a.attempt, a.kind, a.media_type, a.sha256, a.content, a.created_at
		FROM validation_runs v JOIN artifacts a ON a.id = v.artifact_id
		WHERE v.task_id = ? AND v.head_sha = ? AND v.workspace_hash = ? AND v.validator = ?
		AND v.validator_version = ? AND v.config_hash = ? AND v.command_hash = ?
		AND v.environment_hash = ?`, key.TaskID, key.HeadSHA, key.WorkspaceHash, key.Validator,
		key.ValidatorVersion, key.ConfigHash, key.CommandHash, key.EnvironmentHash).Scan(
		&record.ID, &record.Attempt, &record.Key.TaskID, &record.Key.HeadSHA, &record.Key.WorkspaceHash,
		&validatorKind, &record.Key.ValidatorVersion, &record.Key.ConfigHash, &record.Key.CommandHash,
		&record.Key.EnvironmentHash, &record.Status, &created,
		&artifact.ID, &artifact.TaskID, &artifact.Attempt, &artifact.Kind, &artifact.MediaType,
		&artifact.SHA256, &evidence, &artifactCreated)
	if err != nil {
		return validation.Record{}, err
	}
	record.Key.Validator = validation.Kind(validatorKind)
	record.CreatedAt, err = parseTime(created)
	if err == nil {
		artifact.CreatedAt, err = parseTime(artifactCreated)
	}
	if err != nil {
		return validation.Record{}, fmt.Errorf("parse validation timestamp: %w", err)
	}
	artifact.Content = append([]byte(nil), evidence...)
	record.Artifact = artifact
	digest := sha256.Sum256(evidence)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), artifact.SHA256) {
		return validation.Record{}, errors.New("validation artifact digest mismatch")
	}
	if err := json.Unmarshal(evidence, &record.Result); err != nil {
		return validation.Record{}, fmt.Errorf("decode validation evidence: %w", err)
	}
	if err := record.Result.Validate(); err != nil {
		return validation.Record{}, fmt.Errorf("invalid validation evidence: %w", err)
	}
	if record.Result.Status != record.Status {
		return validation.Record{}, errors.New("validation artifact status mismatch")
	}
	return record, nil
}
