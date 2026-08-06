// Package db owns SQLite persistence and atomic projection/event updates.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"parallel-intellect/internal/domain"
	"parallel-intellect/migrations"
)

type Store struct {
	db *sql.DB
}

// Open opens a SQLite database, enables reliability pragmas, and applies all
// embedded forward-only migrations. The path may be ":memory:" for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve control-plane home: %w", err)
		}
		path = filepath.Join(home, ".parallel-intellect", "pintellect.db")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := sqliteDSN(path)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer connection gives commands a deterministic serialization
	// point. Separate Store instances still contend through SQLite's locking.
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return &Store{db: database}, nil
}

func sqliteDSN(path string) string {
	pragmas := "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	if path == ":memory:" {
		return "file::memory:?" + pragmas
	}
	location := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return location.String() + "?" + pragmas
}

func (s *Store) Close() error { return s.db.Close() }

func migrate(ctx context.Context, database *sql.DB) error {
	if _, err := database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied int
		err := database.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", entry.Name()).Scan(&applied)
		if err != nil {
			return fmt.Errorf("inspect migration %s: %w", entry.Name(), err)
		}
		if applied != 0 {
			continue
		}
		body, err := migrations.FS.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO schema_migrations(version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
				entry.Name())
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

var (
	ErrNotFound         = errors.New("not found")
	ErrCommandConflict  = errors.New("command id reused with different request")
	ErrAttemptBudget    = errors.New("mission task-attempt budget exhausted")
	ErrTaskNotRetryable = errors.New("task is not retryable")
	ErrStaleAttempt     = errors.New("task attempt is not current")
	ErrLeaseExists      = errors.New("task attempt already has a Treehouse lease")
	ErrLeaseConflict    = errors.New("Treehouse lease identity or state changed")
	ErrRecoveryPrompted = errors.New("worker recovery prompt already reserved")
)

// ConflictError is returned after a failed conditional update. Current is the
// reloaded authoritative projection; callers decide what to do next.
type ConflictError struct {
	Current domain.Task
}

func (e *ConflictError) Error() string { return "stale task transition" }
