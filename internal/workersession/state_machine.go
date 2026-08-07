// Package workersession defines worker lifecycle policy independently of task
// lifecycle policy.
package workersession

import (
	"errors"
	"fmt"

	"sophon/internal/domain"
)

var ErrIllegalTransition = errors.New("illegal worker-session state transition")

var transitions = map[domain.WorkerSessionState]map[domain.WorkerSessionState]struct{}{
	domain.WorkerSessionStarting: {
		domain.WorkerSessionRunning:  {},
		domain.WorkerSessionLost:     {},
		domain.WorkerSessionFailed:   {},
		domain.WorkerSessionStopping: {},
	},
	domain.WorkerSessionRunning: {
		domain.WorkerSessionIdle:     {},
		domain.WorkerSessionLost:     {},
		domain.WorkerSessionFailed:   {},
		domain.WorkerSessionStopping: {},
	},
	domain.WorkerSessionIdle: {
		domain.WorkerSessionRunning:  {},
		domain.WorkerSessionInactive: {},
		domain.WorkerSessionLost:     {},
		domain.WorkerSessionFailed:   {},
		domain.WorkerSessionStopping: {},
	},
	domain.WorkerSessionInactive: {
		domain.WorkerSessionRunning:  {},
		domain.WorkerSessionLost:     {},
		domain.WorkerSessionFailed:   {},
		domain.WorkerSessionStopping: {},
	},
	domain.WorkerSessionStopping: {
		domain.WorkerSessionStopped: {},
		domain.WorkerSessionLost:    {},
		domain.WorkerSessionFailed:  {},
	},
}

// ValidateTransition does not inspect or mutate a task. In particular, an
// idle worker never implies a task transition.
func ValidateTransition(from, to domain.WorkerSessionState) error {
	if !IsKnownState(from) || !IsKnownState(to) || from == to {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	if _, ok := transitions[from][to]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}

func IsKnownState(state domain.WorkerSessionState) bool {
	switch state {
	case domain.WorkerSessionStarting, domain.WorkerSessionRunning,
		domain.WorkerSessionIdle, domain.WorkerSessionInactive,
		domain.WorkerSessionLost, domain.WorkerSessionFailed,
		domain.WorkerSessionStopping, domain.WorkerSessionStopped:
		return true
	default:
		return false
	}
}
