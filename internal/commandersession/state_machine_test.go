package commandersession

import (
	"errors"
	"testing"

	"parallel-intellect/internal/domain"
)

func TestCommanderSessionTransitions(t *testing.T) {
	for _, transition := range [][2]domain.CommanderSessionState{
		{domain.CommanderSessionStarting, domain.CommanderSessionRunning},
		{domain.CommanderSessionRunning, domain.CommanderSessionIdle},
		{domain.CommanderSessionIdle, domain.CommanderSessionRunning},
		{domain.CommanderSessionRunning, domain.CommanderSessionNeedsAttention},
		{domain.CommanderSessionNeedsAttention, domain.CommanderSessionRunning},
		{domain.CommanderSessionIdle, domain.CommanderSessionStopping},
		{domain.CommanderSessionStopping, domain.CommanderSessionStopped},
	} {
		if err := ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("%s -> %s: %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateTransition(domain.CommanderSessionStopped, domain.CommanderSessionRunning); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("stopped -> running = %v", err)
	}
}
