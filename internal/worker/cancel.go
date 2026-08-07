package worker

import (
	"context"
	"errors"

	"sophon/internal/db"
	"sophon/internal/domain"
	"sophon/internal/herdr"
)

type LeaseReleaser interface {
	Release(context.Context, domain.CommandID, domain.TaskID, int) (domain.TreehouseLease, error)
}

type sessionStopper interface {
	Stop(context.Context, herdr.Session) error
}

// Canceller makes cancellation durable before best-effort runtime cleanup. A
// failed conditional lease return or an already-dead pane is audited but can
// never prevent the operator from reaching cancelled.
type Canceller struct {
	Store     *db.Store
	Treehouse LeaseReleaser
	Herdr     herdr.Adapter
}

func (c *Canceller) Cancel(ctx context.Context, taskID domain.TaskID, commandID domain.CommandID) (domain.Task, error) {
	if c == nil || c.Store == nil || c.Treehouse == nil || c.Herdr == nil {
		return domain.Task{}, errors.New("task canceller is not fully configured")
	}
	before, err := c.Store.Task(ctx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if isTerminal(before.State) {
		return before, nil
	}
	cancelled, err := c.Store.CancelTask(ctx, commandID, taskID, before.Version, "operator")
	if err != nil {
		return domain.Task{}, err
	}
	if lease, leaseErr := c.Store.TreehouseLease(ctx, taskID, before.CurrentAttempt); leaseErr == nil && lease.State == domain.TreehouseLeaseActive {
		if _, releaseErr := c.Treehouse.Release(ctx, commandID+"_lease", taskID, before.CurrentAttempt); releaseErr != nil {
			_, _ = c.Store.FenceTreehouseLeaseAfterReleaseFailure(ctx, commandID+"_lease_failure", db.ReleaseTreehouseLeaseInput{TaskID: taskID, Attempt: before.CurrentAttempt, LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder, Actor: "operator"}, releaseErr.Error())
		}
	}
	if session, sessionErr := c.Store.WorkerSession(ctx, taskID, before.CurrentAttempt); sessionErr == nil {
		if observed, observeErr := c.Herdr.Observe(ctx, runtimeSession(session)); observeErr == nil && observed != herdr.StateLost && observed != herdr.StateHusk {
			stopping, transitionErr := c.Store.TransitionWorkerSession(ctx, commandID+"_stopping", db.TransitionWorkerSessionInput{SessionID: session.ID, TaskID: taskID, Attempt: before.CurrentAttempt, ExpectedState: session.State, ExpectedVersion: session.Version, To: domain.WorkerSessionStopping, Actor: "operator"})
			if transitionErr == nil {
				if stopper, ok := c.Herdr.(sessionStopper); ok {
					if stopErr := stopper.Stop(ctx, runtimeSession(stopping)); stopErr == nil {
						_, _ = c.Store.TransitionWorkerSession(ctx, commandID+"_stopped", db.TransitionWorkerSessionInput{SessionID: stopping.ID, TaskID: taskID, Attempt: before.CurrentAttempt, ExpectedState: stopping.State, ExpectedVersion: stopping.Version, To: domain.WorkerSessionStopped, Actor: "operator"})
					}
				}
			}
		}
	}
	return cancelled, nil
}

func isTerminal(state domain.TaskState) bool {
	switch state {
	case domain.TaskDelivered, domain.TaskDeliveredBranch, domain.TaskReportReady, domain.TaskCancelled, domain.TaskFailed:
		return true
	default:
		return false
	}
}
