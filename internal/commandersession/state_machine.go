// Package commandersession owns commander lifecycle policy independently of
// mission and worker lifecycle policy.
package commandersession

import (
	"errors"
	"fmt"

	"sophon/internal/domain"
)

var ErrIllegalTransition = errors.New("illegal commander-session state transition")

var transitions = map[domain.CommanderSessionState]map[domain.CommanderSessionState]struct{}{
	domain.CommanderSessionStarting: {
		domain.CommanderSessionRunning: {}, domain.CommanderSessionIdle: {},
		domain.CommanderSessionNeedsAttention: {}, domain.CommanderSessionFailed: {},
		domain.CommanderSessionStopping: {},
	},
	domain.CommanderSessionRunning: {
		domain.CommanderSessionIdle: {}, domain.CommanderSessionNeedsAttention: {},
		domain.CommanderSessionFailed: {}, domain.CommanderSessionStopping: {},
	},
	domain.CommanderSessionIdle: {
		domain.CommanderSessionRunning: {}, domain.CommanderSessionNeedsAttention: {},
		domain.CommanderSessionFailed: {}, domain.CommanderSessionStopping: {},
	},
	domain.CommanderSessionNeedsAttention: {
		domain.CommanderSessionRunning: {}, domain.CommanderSessionIdle: {},
		domain.CommanderSessionFailed: {}, domain.CommanderSessionStopping: {}, domain.CommanderSessionStopped: {},
	},
	domain.CommanderSessionStopping: {
		domain.CommanderSessionStopped: {}, domain.CommanderSessionNeedsAttention: {},
		domain.CommanderSessionFailed: {},
	},
}

func ValidateTransition(from, to domain.CommanderSessionState) error {
	if !IsKnownState(from) || !IsKnownState(to) || from == to {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	if _, ok := transitions[from][to]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}

func IsKnownState(state domain.CommanderSessionState) bool {
	switch state {
	case domain.CommanderSessionStarting, domain.CommanderSessionRunning,
		domain.CommanderSessionIdle, domain.CommanderSessionNeedsAttention,
		domain.CommanderSessionFailed, domain.CommanderSessionStopping,
		domain.CommanderSessionStopped:
		return true
	default:
		return false
	}
}
