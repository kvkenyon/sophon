package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sophon/internal/domain"
)

func runCommand[T any](ctx context.Context, s *Store, commandID domain.CommandID, kind string, request any, mutate func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	if commandID == "" {
		return zero, errors.New("command id is required")
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return zero, fmt.Errorf("marshal command request: %w", err)
	}
	digest := sha256.Sum256(requestJSON)
	requestHash := hex.EncodeToString(digest[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("begin command: %w", err)
	}
	defer tx.Rollback()

	var existingKind, existingHash, status string
	var resultJSON []byte
	err = tx.QueryRowContext(ctx,
		"SELECT kind, request_hash, status, result_json FROM commands WHERE id = ?", commandID,
	).Scan(&existingKind, &existingHash, &status, &resultJSON)
	if err == nil {
		if existingKind != kind || existingHash != requestHash {
			return zero, ErrCommandConflict
		}
		if status != "completed" {
			return zero, fmt.Errorf("command %s has unexpected status %q", commandID, status)
		}
		var result T
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return zero, fmt.Errorf("decode stored command result: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("lookup command: %w", err)
	}

	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO commands(id, kind, request_hash, status, created_at) VALUES (?, ?, ?, 'running', ?)",
		commandID, kind, requestHash, now); err != nil {
		return zero, fmt.Errorf("record command: %w", err)
	}

	result, err := mutate(tx)
	if err != nil {
		return zero, err
	}
	resultJSON, err = json.Marshal(result)
	if err != nil {
		return zero, fmt.Errorf("marshal command result: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE commands SET status = 'completed', result_json = ?, completed_at = ? WHERE id = ?",
		resultJSON, formatTime(time.Now()), commandID); err != nil {
		return zero, fmt.Errorf("complete command: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("commit command: %w", err)
	}
	return result, nil
}
