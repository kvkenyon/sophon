// Package validation runs configured commands in a task worktree and
// structures their pass/fail evidence.
package validation

import (
	"context"
	"errors"
	"fmt"
	"time"
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
