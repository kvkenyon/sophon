// Package signals defines the durable operator-signal model and lifecycle policy.
package signals

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
)

type SignalID string
type SignalKind string
type SignalStatus string

const (
	SignalDecision           SignalKind = "decision"
	SignalCredential         SignalKind = "credential"
	SignalPermission         SignalKind = "permission"
	SignalMissingContext     SignalKind = "missing-context"
	SignalEnvironment        SignalKind = "environment"
	SignalExternalDependency SignalKind = "external-dependency"
	SignalConflict           SignalKind = "conflict"
	SignalUnsafeOperation    SignalKind = "unsafe-operation"

	SignalOpen     SignalStatus = "open"
	SignalResolved SignalStatus = "resolved"
)

// Option is one possible operator answer. Value is the stable answer value;
// Description supplies any additional operator-facing context.
type Option struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// Signal is the durable identity and current projection of an operator
// question. Version is used for compare-and-swap resolution.
type Signal struct {
	ID             SignalID         `json:"id"`
	MissionID      domain.MissionID `json:"mission_id"`
	TaskID         *domain.TaskID   `json:"task_id,omitempty"`
	Kind           SignalKind       `json:"kind"`
	Question       string           `json:"question"`
	Context        string           `json:"context"`
	Options        []Option         `json:"options"`
	Recommendation string           `json:"recommendation"`
	Status         SignalStatus     `json:"status"`
	Answer         *string          `json:"answer,omitempty"`
	Version        int64            `json:"version"`
	CreatedAt      time.Time        `json:"created_at"`
	ResolvedAt     *time.Time       `json:"resolved_at,omitempty"`
}

var ErrIllegalTransition = errors.New("illegal signal state transition")

// ValidateTransition permits the single lifecycle transition specified for
// V1. V1 has no answerless close path.
func ValidateTransition(from, to SignalStatus) error {
	if from == SignalOpen && to == SignalResolved {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
}

func ValidateNew(missionID domain.MissionID, taskID *domain.TaskID, kind SignalKind, question string) error {
	if missionID == "" || strings.TrimSpace(string(kind)) == "" || strings.TrimSpace(question) == "" {
		return errors.New("mission, kind, and question are required")
	}
	if taskID != nil && *taskID == "" {
		return errors.New("task id cannot be empty")
	}
	return nil
}
