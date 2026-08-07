// Package recovery coordinates startup reconciliation across durable task
// state and the existing Treehouse, Herdr, validation, and delivery boundaries.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/delivery"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/id"
	taskpolicy "parallel-intellect/internal/task"
	"parallel-intellect/internal/treehouse"
	"parallel-intellect/internal/worker"
)

type Outcome string

const (
	OutcomeExactlyOnce Outcome = "exactly_once"
	OutcomeRecoverable Outcome = "explicit_recoverable"
)

type Status string

const (
	StatusQueued                Status = "queued"
	StatusAwaitingLease         Status = "awaiting_lease"
	StatusAwaitingWorkerStart   Status = "awaiting_worker_start"
	StatusWorkerObserved        Status = "worker_observed"
	StatusWorkerInactive        Status = "worker_inactive"
	StatusWorkerMissing         Status = "worker_missing"
	StatusCompletionStabilizing Status = "completion_stabilizing"
	StatusCompletionResumed     Status = "completion_resumed"
	StatusFailureRecorded       Status = "failure_recorded"
	StatusBlockerRecorded       Status = "blocker_recorded"
	StatusReady                 Status = "ready"
	StatusValidationResumable   Status = "validation_resumable"
	StatusDeliveryPending       Status = "delivery_pending"
	StatusDeliveryResumed       Status = "delivery_resumed"
	StatusNeedsAttention        Status = "needs_attention"
	StatusCancelling            Status = "cancelling"
)

type TaskResult struct {
	TaskID  domain.TaskID    `json:"task_id"`
	Attempt int              `json:"attempt"`
	State   domain.TaskState `json:"state"`
	Status  Status           `json:"status"`
	Outcome Outcome          `json:"outcome"`
	Error   string           `json:"error,omitempty"`
}

type Report struct {
	Leases treehouse.ReconcileResult `json:"leases"`
	Tasks  []TaskResult              `json:"tasks"`
}

type LeaseReconciler interface {
	Reconcile(context.Context) (treehouse.ReconcileResult, error)
}

type WorkerReconciler interface {
	Reconcile(context.Context, domain.TaskID) (worker.RecoveryResult, error)
}

type CompletionResumer interface {
	Resume(context.Context, domain.TaskID) (domain.Task, error)
}

type DeliveryReconciler interface {
	Reconcile(context.Context, domain.TaskID, int) (delivery.Result, error)
}

type Service struct {
	Store      *db.Store
	Leases     LeaseReconciler
	Worker     func(domain.WorkerSession) WorkerReconciler
	Completion CompletionResumer
	Delivery   DeliveryReconciler
	Now        func() time.Time
}

// Reconcile executes one restart pass. Per-task external failures are retained
// in the report so one broken task cannot hide the recoverable state of the
// others. A global lease/list failure aborts because task identity cannot then
// be reconciled safely.
func (s *Service) Reconcile(ctx context.Context) (Report, error) {
	if s == nil || s.Store == nil || s.Leases == nil {
		return Report{}, errors.New("startup reconciler is not fully configured")
	}
	leaseReport, err := s.Leases.Reconcile(ctx)
	if err != nil {
		return Report{}, err
	}
	tasks, err := s.Store.NonterminalTasks(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Leases: leaseReport, Tasks: make([]TaskResult, 0, len(tasks))}
	for _, task := range tasks {
		result := s.reconcileTask(ctx, task)
		report.Tasks = append(report.Tasks, result)
	}
	return report, nil
}

func (s *Service) reconcileTask(ctx context.Context, task domain.Task) TaskResult {
	result := TaskResult{TaskID: task.ID, Attempt: task.CurrentAttempt, State: task.State,
		Outcome: OutcomeRecoverable}
	current, err := s.Store.Task(ctx, task.ID)
	if err != nil {
		return withError(result, err)
	}
	if taskpolicy.IsTerminal(current.State) {
		result.State, result.Status, result.Outcome = current.State, StatusReady, OutcomeExactlyOnce
		return result
	}
	result.State = current.State
	if s.inFlightStabilizing(current) {
		switch current.State {
		case domain.TaskStarting:
			result.Status = StatusAwaitingWorkerStart
		case domain.TaskCollecting:
			result.Status = StatusCompletionStabilizing
		}
		return result
	}

	lease, leaseErr := s.Store.TreehouseLease(ctx, current.ID, current.CurrentAttempt)
	if errors.Is(leaseErr, db.ErrNotFound) {
		switch current.State {
		case domain.TaskQueued:
			result.Status = StatusQueued
		case domain.TaskProvisioning:
			result.Status = StatusAwaitingLease
		default:
			return s.escalateMissingWorker(ctx, current, result,
				"task has no durable current-attempt lease during startup")
		}
		return result
	}
	if leaseErr != nil {
		return withError(result, leaseErr)
	}
	if lease.State != domain.TreehouseLeaseActive {
		return withError(result, fmt.Errorf("current lease is %s", lease.State))
	}

	session, sessionErr := s.Store.WorkerSession(ctx, current.ID, current.CurrentAttempt)
	if errors.Is(sessionErr, db.ErrNotFound) {
		switch current.State {
		case domain.TaskProvisioning:
			result.Status = StatusAwaitingWorkerStart
			return result
		case domain.TaskReady:
			result.Status, result.Outcome = StatusReady, OutcomeExactlyOnce
			return result
		case domain.TaskValidating:
			return s.reconcileValidationOrDelivery(ctx, current, result)
		case domain.TaskDeliveryBlocked, domain.TaskNeedsAttention:
			result.Status = StatusNeedsAttention
			return result
		case domain.TaskCancelling:
			result.Status = StatusCancelling
			return result
		default:
			return s.escalateMissingWorker(ctx, current, result,
				"expected current-attempt worker session is missing from durable state")
		}
	}
	if sessionErr != nil {
		return withError(result, sessionErr)
	}
	if s.Worker == nil {
		return withError(result, errors.New("worker reconciliation is not configured"))
	}
	observed, err := s.Worker(session).Reconcile(ctx, current.ID)
	if err != nil {
		return withError(result, err)
	}
	result.State = observed.Task.State
	switch observed.Status {
	case worker.RecoveryStructured:
		switch observed.Outcome {
		case worker.OutcomeCompletion:
			if s.Completion == nil {
				return withError(result, errors.New("completion recovery is not configured"))
			}
			completed, err := s.Completion.Resume(ctx, current.ID)
			if err != nil {
				return withError(result, err)
			}
			result.State, result.Status, result.Outcome = completed.State, StatusCompletionResumed, OutcomeExactlyOnce
			return result
		case worker.OutcomeFailure:
			return s.recordStructuredOutcome(ctx, observed.Task, result, domain.TaskFailed, StatusFailureRecorded)
		case worker.OutcomeBlocker:
			return s.recordStructuredOutcome(ctx, observed.Task, result, domain.TaskBlocked, StatusBlockerRecorded)
		}
	case worker.RecoveryLost, worker.RecoveryNeedsAttention:
		result.Status = StatusWorkerMissing
		return result
	case worker.RecoveryInactive, worker.RecoveryIdle, worker.RecoveryStabilizing,
		worker.RecoveryWaiting, worker.RecoveryPromptSent:
		result.Status = StatusWorkerInactive
	default:
		result.Status, result.Outcome = StatusWorkerObserved, OutcomeExactlyOnce
	}

	current, err = s.Store.Task(ctx, current.ID)
	if err != nil {
		return withError(result, err)
	}
	result.State = current.State
	if current.State == domain.TaskValidating {
		return s.reconcileValidationOrDelivery(ctx, current, result)
	}
	if current.State == domain.TaskReady {
		result.Status, result.Outcome = StatusReady, OutcomeExactlyOnce
	}
	return result
}

func (s *Service) inFlightStabilizing(task domain.Task) bool {
	if task.State != domain.TaskStarting && task.State != domain.TaskCollecting {
		return false
	}
	return s.now().Sub(task.UpdatedAt) < taskpolicy.InFlightStabilizationWindow
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) reconcileValidationOrDelivery(ctx context.Context, task domain.Task, result TaskResult) TaskResult {
	record, err := s.Store.Delivery(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return withError(result, err)
	}
	if record == nil {
		result.Status = StatusValidationResumable
		return result
	}
	if s.Delivery == nil {
		return withError(result, errors.New("delivery recovery is not configured"))
	}
	resumed, err := s.Delivery.Reconcile(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		if errors.Is(err, delivery.ErrGateRecoveryRequired) {
			result.Status = StatusDeliveryPending
			result.Error = err.Error()
			return result
		}
		return withError(result, err)
	}
	result.State, result.Status, result.Outcome = resumed.Task.State, StatusDeliveryResumed, OutcomeExactlyOnce
	return result
}

func (s *Service) escalateMissingWorker(ctx context.Context, task domain.Task, result TaskResult, reason string) TaskResult {
	if task.State == domain.TaskNeedsAttention {
		result.Status = StatusNeedsAttention
		return result
	}
	updated, err := s.transition(ctx, task, domain.TaskNeedsAttention, "recovery")
	if err != nil {
		return withError(result, fmt.Errorf("%s: %w", reason, err))
	}
	result.State, result.Status = updated.State, StatusWorkerMissing
	return result
}

func (s *Service) recordStructuredOutcome(ctx context.Context, task domain.Task, result TaskResult,
	to domain.TaskState, status Status) TaskResult {
	updated, err := s.transition(ctx, task, to, "worker-recovery")
	if err != nil {
		return withError(result, err)
	}
	result.State, result.Status, result.Outcome = updated.State, status, OutcomeExactlyOnce
	return result
}

func (s *Service) transition(ctx context.Context, task domain.Task, to domain.TaskState, actor string) (domain.Task, error) {
	raw, err := id.New("cmd")
	if err != nil {
		return domain.Task{}, err
	}
	return s.Store.TransitionTask(ctx, domain.CommandID(raw), db.TransitionTaskInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State,
		ExpectedVersion: task.Version, To: to, Actor: actor,
	})
}

func withError(result TaskResult, err error) TaskResult {
	result.Error = err.Error()
	return result
}
