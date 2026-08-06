// Package validation runs content-addressed checks against immutable task
// attempt inputs and retains their structured evidence.
package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
)

type Kind string

const (
	UnitTests         Kind = "unit_tests"
	Typecheck         Kind = "typecheck"
	Lint              Kind = "lint"
	ProjectValidation Kind = "project_validation"
)

type Status string

const (
	Passed Status = "passed"
	Failed Status = "failed"
)

// Validator describes one versioned command and executes it in a worktree.
// Command must fully identify the invocation for cache-key purposes.
type Validator interface {
	Kind() Kind
	Version() string
	Command() []string
	Run(context.Context, string) (Result, error)
}

type Result struct {
	Status    Status        `json:"status"`
	ExitCode  int           `json:"exit_code"`
	Output    string        `json:"output"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
}

func (r Result) Validate() error { return validateResult(r) }

type Key struct {
	TaskID           domain.TaskID `json:"task_id"`
	HeadSHA          string        `json:"head_sha"`
	WorkspaceHash    string        `json:"workspace_hash"`
	Validator        Kind          `json:"validator"`
	ValidatorVersion string        `json:"validator_version"`
	ConfigHash       string        `json:"config_hash"`
	CommandHash      string        `json:"command_hash"`
	EnvironmentHash  string        `json:"environment_hash"`
}

func (k Key) Validate() error {
	if k.TaskID == "" || strings.TrimSpace(k.HeadSHA) == "" || strings.TrimSpace(k.WorkspaceHash) == "" ||
		strings.TrimSpace(string(k.Validator)) == "" || strings.TrimSpace(k.ValidatorVersion) == "" ||
		strings.TrimSpace(k.ConfigHash) == "" || strings.TrimSpace(k.CommandHash) == "" ||
		strings.TrimSpace(k.EnvironmentHash) == "" {
		return errors.New("complete validation cache key is required")
	}
	return validateKind(k.Validator)
}

func (k Key) Digest() string {
	encoded, _ := json.Marshal(k)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type Artifact struct {
	ID        domain.ArtifactID `json:"id"`
	TaskID    domain.TaskID     `json:"task_id"`
	Attempt   int               `json:"attempt"`
	Kind      string            `json:"kind"`
	MediaType string            `json:"media_type"`
	SHA256    string            `json:"sha256"`
	Content   []byte            `json:"content"`
	CreatedAt time.Time         `json:"created_at"`
}

type Record struct {
	ID        string    `json:"id"`
	Attempt   int       `json:"attempt"`
	Key       Key       `json:"key"`
	Status    Status    `json:"status"`
	Result    Result    `json:"result"`
	Artifact  Artifact  `json:"artifact"`
	CreatedAt time.Time `json:"created_at"`
}

type RecordInput struct {
	TaskID  domain.TaskID `json:"task_id"`
	Attempt int           `json:"attempt"`
	Key     Key           `json:"key"`
	Result  Result        `json:"result"`
}

type BeginInput struct {
	TaskID  domain.TaskID `json:"task_id"`
	Attempt int           `json:"attempt"`
	Actor   string        `json:"actor"`
}

type CompleteInput struct {
	TaskID          domain.TaskID `json:"task_id"`
	Attempt         int           `json:"attempt"`
	ExpectedVersion int64         `json:"expected_version"`
	HeadSHA         string        `json:"head_sha"`
	WorkspaceHash   string        `json:"workspace_hash"`
	ConfigHash      string        `json:"config_hash"`
	EnvironmentHash string        `json:"environment_hash"`
	RunIDs          []string      `json:"run_ids"`
	Actor           string        `json:"actor"`
}

type Workspace struct {
	HeadSHA       string
	DirtyTreeHash string
}

type WorkspaceFingerprinter interface {
	Fingerprint(context.Context, string) (Workspace, error)
}

type EnvironmentFingerprinter interface {
	Fingerprint() (string, error)
}

type Store interface {
	Task(context.Context, domain.TaskID) (domain.Task, error)
	Attempt(context.Context, domain.TaskID, int) (domain.TaskAttempt, error)
	BeginValidation(context.Context, domain.CommandID, BeginInput) (domain.Task, error)
	LookupValidation(context.Context, Key) (*Record, error)
	RecordValidation(context.Context, domain.CommandID, RecordInput) (Record, error)
	CompleteValidation(context.Context, domain.CommandID, CompleteInput) (domain.Task, error)
}

func validateKind(kind Kind) error {
	switch kind {
	case UnitTests, Typecheck, Lint, ProjectValidation:
		return nil
	default:
		return fmt.Errorf("unknown validator kind %q", kind)
	}
}

func validateResult(result Result) error {
	if result.StartedAt.IsZero() || result.Duration < 0 {
		return errors.New("validation result requires a start time and non-negative duration")
	}
	switch result.Status {
	case Passed:
		if result.ExitCode != 0 {
			return errors.New("passed validation must have exit code zero")
		}
	case Failed:
		if result.ExitCode == 0 {
			return errors.New("failed validation must have a non-zero exit code")
		}
	default:
		return fmt.Errorf("unknown validation status %q", result.Status)
	}
	return nil
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
