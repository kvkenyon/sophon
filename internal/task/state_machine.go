// Package task defines deterministic task lifecycle policy.
package task

import (
	"errors"
	"fmt"
	"time"

	"parallel-intellect/internal/domain"
)

// InFlightStabilizationWindow prevents recovery from treating a recently
// started launch or completion handoff as abandoned work.
const InFlightStabilizationWindow = 30 * time.Second

var ErrIllegalTransition = errors.New("illegal task state transition")

var commonTransitions = map[domain.TaskState]map[domain.TaskState]struct{}{
	domain.TaskQueued:       {domain.TaskProvisioning: {}},
	domain.TaskProvisioning: {domain.TaskStarting: {}},
	domain.TaskStarting:     {domain.TaskRunning: {}},
	domain.TaskRunning: {
		domain.TaskBlocked:    {},
		domain.TaskCollecting: {},
	},
	domain.TaskBlocked: {
		domain.TaskRunning: {},
	},
	domain.TaskValidating: {
		domain.TaskDelivered:       {},
		domain.TaskDeliveryBlocked: {},
	},
	domain.TaskDeliveryBlocked: {
		domain.TaskValidating: {},
	},
	domain.TaskCancelling: {
		domain.TaskCancelled: {},
	},
}

// ValidateTransition checks both the graph and task-kind/delivery-mode guards.
func ValidateTransition(t domain.Task, to domain.TaskState) error {
	from := t.State
	if !IsKnownState(from) || !IsKnownState(to) {
		return fmt.Errorf("%w: unknown state in %s -> %s", ErrIllegalTransition, from, to)
	}
	if from == to || IsTerminal(from) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}

	if !IsTerminal(from) {
		switch to {
		case domain.TaskNeedsAttention, domain.TaskCancelling, domain.TaskFailed:
			return nil
		}
	}

	if from == domain.TaskCollecting {
		if t.Kind == domain.TaskScout && to == domain.TaskReportReady {
			return nil
		}
		if t.Kind != domain.TaskScout && to == domain.TaskReady {
			return nil
		}
	}

	if from == domain.TaskReady {
		if t.Kind == domain.TaskScout {
			return fmt.Errorf("%w: scout tasks do not enter ready", ErrIllegalTransition)
		}
		if t.DeliveryMode == domain.DeliveryBranch && to == domain.TaskDeliveredBranch {
			return nil
		}
		if t.DeliveryMode != domain.DeliveryBranch && to == domain.TaskValidating {
			return nil
		}
	}

	if _, ok := commonTransitions[from][to]; ok {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
}

func IsKnownState(state domain.TaskState) bool {
	switch state {
	case domain.TaskQueued, domain.TaskProvisioning, domain.TaskStarting, domain.TaskRunning,
		domain.TaskBlocked, domain.TaskCollecting, domain.TaskReady, domain.TaskReportReady,
		domain.TaskValidating, domain.TaskDeliveryBlocked, domain.TaskDelivered,
		domain.TaskDeliveredBranch, domain.TaskNeedsAttention, domain.TaskCancelling,
		domain.TaskCancelled, domain.TaskFailed:
		return true
	default:
		return false
	}
}

func IsTerminal(state domain.TaskState) bool {
	switch state {
	case domain.TaskDelivered, domain.TaskDeliveredBranch, domain.TaskReportReady,
		domain.TaskCancelled, domain.TaskFailed:
		return true
	default:
		return false
	}
}

func IsRetryable(state domain.TaskState) bool {
	return !IsTerminal(state) || state == domain.TaskFailed || state == domain.TaskCancelled || state == domain.TaskNeedsAttention
}
