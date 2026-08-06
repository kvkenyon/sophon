package workersession

import (
	"errors"
	"testing"

	"parallel-intellect/internal/domain"
)

func TestStateMachine(t *testing.T) {
	allowed := [][2]domain.WorkerSessionState{
		{domain.WorkerSessionStarting, domain.WorkerSessionRunning},
		{domain.WorkerSessionRunning, domain.WorkerSessionIdle},
		{domain.WorkerSessionIdle, domain.WorkerSessionInactive},
		{domain.WorkerSessionInactive, domain.WorkerSessionRunning},
		{domain.WorkerSessionRunning, domain.WorkerSessionLost},
		{domain.WorkerSessionRunning, domain.WorkerSessionFailed},
		{domain.WorkerSessionRunning, domain.WorkerSessionStopping},
		{domain.WorkerSessionStopping, domain.WorkerSessionStopped},
	}
	for _, transition := range allowed {
		if err := ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("%s -> %s: %v", transition[0], transition[1], err)
		}
	}

	for _, transition := range [][2]domain.WorkerSessionState{
		{domain.WorkerSessionRunning, domain.WorkerSessionInactive},
		{domain.WorkerSessionInactive, domain.WorkerSessionIdle},
		{domain.WorkerSessionStopped, domain.WorkerSessionRunning},
		{domain.WorkerSessionLost, domain.WorkerSessionRunning},
		{domain.WorkerSessionIdle, domain.WorkerSessionIdle},
	} {
		if err := ValidateTransition(transition[0], transition[1]); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s -> %s error = %v", transition[0], transition[1], err)
		}
	}
}
