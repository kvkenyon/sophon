package task

import (
	"errors"
	"testing"

	"sophon/internal/domain"
)

func TestStateMachineLegalPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		task domain.Task
		to   domain.TaskState
	}{
		{"normal start", domain.Task{State: domain.TaskQueued}, domain.TaskProvisioning},
		{"block", domain.Task{State: domain.TaskRunning}, domain.TaskBlocked},
		{"resume", domain.Task{State: domain.TaskBlocked}, domain.TaskRunning},
		{"collect implementation", domain.Task{State: domain.TaskRunning}, domain.TaskCollecting},
		{"implementation ready", domain.Task{State: domain.TaskCollecting, Kind: domain.TaskImplementation}, domain.TaskReady},
		{"scout report", domain.Task{State: domain.TaskCollecting, Kind: domain.TaskScout}, domain.TaskReportReady},
		{"branch completion", domain.Task{State: domain.TaskReady, Kind: domain.TaskImplementation, DeliveryMode: domain.DeliveryBranch}, domain.TaskDeliveredBranch},
		{"gate validation", domain.Task{State: domain.TaskReady, Kind: domain.TaskImplementation, DeliveryMode: domain.DeliveryGate}, domain.TaskValidating},
		{"validation blocker", domain.Task{State: domain.TaskValidating}, domain.TaskDeliveryBlocked},
		{"validation resume", domain.Task{State: domain.TaskDeliveryBlocked}, domain.TaskValidating},
		{"cancel", domain.Task{State: domain.TaskRunning}, domain.TaskCancelling},
		{"cancelled", domain.Task{State: domain.TaskCancelling}, domain.TaskCancelled},
		{"attention", domain.Task{State: domain.TaskProvisioning}, domain.TaskNeedsAttention},
		{"fail", domain.Task{State: domain.TaskBlocked}, domain.TaskFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateTransition(tt.task, tt.to); err != nil {
				t.Fatalf("transition rejected: %v", err)
			}
		})
	}
}

func TestStateMachineRejectsIllegalAndCrossKindPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		task domain.Task
		to   domain.TaskState
	}{
		{"skip", domain.Task{State: domain.TaskQueued}, domain.TaskRunning},
		{"self", domain.Task{State: domain.TaskRunning}, domain.TaskRunning},
		{"scout ready", domain.Task{State: domain.TaskCollecting, Kind: domain.TaskScout}, domain.TaskReady},
		{"implementation report", domain.Task{State: domain.TaskCollecting, Kind: domain.TaskImplementation}, domain.TaskReportReady},
		{"branch validation", domain.Task{State: domain.TaskReady, Kind: domain.TaskImplementation, DeliveryMode: domain.DeliveryBranch}, domain.TaskValidating},
		{"gate branch completion", domain.Task{State: domain.TaskReady, Kind: domain.TaskImplementation, DeliveryMode: domain.DeliveryGate}, domain.TaskDeliveredBranch},
		{"terminal escape", domain.Task{State: domain.TaskDelivered}, domain.TaskFailed},
		{"unknown source", domain.Task{State: "mystery"}, domain.TaskFailed},
		{"unknown target", domain.Task{State: domain.TaskRunning}, "mystery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateTransition(tt.task, tt.to); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("got %v, want illegal transition", err)
			}
		})
	}
}
