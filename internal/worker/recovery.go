package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sophon/internal/db"
	"sophon/internal/domain"
	"sophon/internal/herdr"
	taskpolicy "sophon/internal/task"
)

const DefaultRecoveryPrompt = "Your Herdr session became idle without a structured task outcome. Re-check the current attempt and write its structured completion, failure, or blocker artifact. Terminal prose is not an outcome."

type OutcomeKind string

const (
	OutcomeNone       OutcomeKind = "none"
	OutcomeCompletion OutcomeKind = "completion"
	OutcomeFailure    OutcomeKind = "failure"
	OutcomeBlocker    OutcomeKind = "blocker"
)

type OutcomeInspector interface {
	Inspect(context.Context, domain.Task, domain.TaskAttempt) (OutcomeKind, error)
}

// ResultFileInspector recognizes only attempt-scoped structured JSON. It never
// reads terminal output or treats prose as completion evidence.
type ResultFileInspector struct {
	TaskFiles BriefGenerator
}

func (i ResultFileInspector) Inspect(_ context.Context, task domain.Task, attempt domain.TaskAttempt) (OutcomeKind, error) {
	dir, err := i.TaskFiles.AttemptDir(task.ID, attempt.Attempt)
	if err != nil {
		return OutcomeNone, err
	}
	path := filepath.Join(dir, "result.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return OutcomeNone, nil
	}
	if err != nil {
		return OutcomeNone, fmt.Errorf("inspect structured worker outcome: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return OutcomeNone, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return OutcomeNone, fmt.Errorf("read structured worker outcome: %w", err)
	}
	var envelope struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return OutcomeNone, nil
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Status)) {
	case "completed":
		return OutcomeCompletion, nil
	case "failed":
		return OutcomeFailure, nil
	case "blocked", "blocker":
		return OutcomeBlocker, nil
	default:
		return OutcomeNone, nil
	}
}

type RecoveryStatus string

const (
	RecoveryRunning        RecoveryStatus = "running"
	RecoveryStabilizing    RecoveryStatus = "stabilizing"
	RecoveryStructured     RecoveryStatus = "structured_outcome"
	RecoveryPromptSent     RecoveryStatus = "recovery_prompt_sent"
	RecoveryWaiting        RecoveryStatus = "waiting_for_recovery"
	RecoveryNeedsAttention RecoveryStatus = "needs_attention"
	RecoveryLost           RecoveryStatus = "lost"
	RecoveryIdle           RecoveryStatus = "idle"
	RecoveryInactive       RecoveryStatus = "inactive"
)

type RecoveryResult struct {
	Status        RecoveryStatus       `json:"status"`
	Outcome       OutcomeKind          `json:"outcome,omitempty"`
	Task          domain.Task          `json:"task"`
	WorkerSession domain.WorkerSession `json:"worker_session"`
}

type Reconciler struct {
	Store              *db.Store
	Herdr              herdr.Adapter
	Outcomes           OutcomeInspector
	StabilizationDelay time.Duration
	RecoveryWait       time.Duration
	RecoveryPrompt     string
	Now                func() time.Time
}

// Reconcile performs one durable observation step. Delays are represented by
// persisted timestamps, so callers can poll without holding a goroutine and a
// daemon restart cannot reset or duplicate recovery.
func (r *Reconciler) Reconcile(ctx context.Context, taskID domain.TaskID) (RecoveryResult, error) {
	if r == nil || r.Store == nil || r.Herdr == nil || r.Outcomes == nil {
		return RecoveryResult{}, errors.New("worker reconciler is not fully configured")
	}
	task, err := r.Store.Task(ctx, taskID)
	if err != nil {
		return RecoveryResult{}, err
	}
	session, err := r.Store.WorkerSession(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return RecoveryResult{}, err
	}
	if session.State == domain.WorkerSessionLost {
		return RecoveryResult{Status: RecoveryLost, Task: task, WorkerSession: session}, nil
	}
	observed, err := r.Herdr.Observe(ctx, runtimeSession(session))
	if err != nil {
		return RecoveryResult{}, err
	}
	if observed == herdr.StateLost {
		commandID, commandErr := newCommandID()
		if commandErr != nil {
			return RecoveryResult{}, commandErr
		}
		updatedTask, reconcileErr := r.Store.ReconcileLostWorker(ctx, commandID, db.ReconcileLostWorkerInput{
			SessionID: session.ID, TaskID: task.ID, Attempt: task.CurrentAttempt,
			ExpectedState: session.State, ExpectedVersion: session.Version, TaskVersion: task.Version,
			Reason: "expected Herdr session is missing", Actor: "reconciler",
		})
		if reconcileErr != nil {
			return RecoveryResult{}, reconcileErr
		}
		updatedSession, loadErr := r.Store.WorkerSession(ctx, task.ID, task.CurrentAttempt)
		return RecoveryResult{Status: RecoveryLost, Task: updatedTask, WorkerSession: updatedSession}, loadErr
	}
	if observed == herdr.StateHusk {
		switch session.State {
		case domain.WorkerSessionRunning:
			session, err = r.transition(ctx, session, domain.WorkerSessionIdle)
			if err == nil {
				session, err = r.transition(ctx, session, domain.WorkerSessionInactive)
			}
		case domain.WorkerSessionIdle:
			session, err = r.transition(ctx, session, domain.WorkerSessionInactive)
		case domain.WorkerSessionInactive:
		default:
			return RecoveryResult{}, fmt.Errorf("Herdr husk conflicts with worker-session state %s", session.State)
		}
		if err != nil {
			return RecoveryResult{}, err
		}
		if task.State != domain.TaskRunning {
			return RecoveryResult{Status: RecoveryInactive, Task: task, WorkerSession: session}, nil
		}
		// Continue into forgotten-completion recovery. If an outcome remains
		// absent, Wake will relaunch the persisted Codex session in this pane.
		observed = herdr.StateIdle
	}

	if observed == herdr.StateRunning {
		if session.State == domain.WorkerSessionIdle || session.State == domain.WorkerSessionInactive {
			session, err = r.transition(ctx, session, domain.WorkerSessionRunning)
			if err != nil {
				return RecoveryResult{}, err
			}
		}
		return RecoveryResult{Status: RecoveryRunning, Task: task, WorkerSession: session}, nil
	}
	if observed != herdr.StateIdle {
		return RecoveryResult{}, fmt.Errorf("unsupported Herdr state %q", observed)
	}
	if session.State == domain.WorkerSessionRunning {
		session, err = r.transition(ctx, session, domain.WorkerSessionIdle)
		if err != nil {
			return RecoveryResult{}, err
		}
	}

	// Idle observation alone changes no task state. Recovery applies only while
	// the task is actively awaiting a structured worker outcome.
	if task.State != domain.TaskRunning {
		return RecoveryResult{Status: RecoveryIdle, Task: task, WorkerSession: session}, nil
	}
	now := r.now()
	idleAt := session.IdleAt
	if idleAt == nil {
		idleAt = session.InactiveAt
	}
	if idleAt == nil {
		idleAt = &session.UpdatedAt
	}
	if now.Sub(*idleAt) < r.stabilizationDelay() {
		return RecoveryResult{Status: RecoveryStabilizing, Task: task, WorkerSession: session}, nil
	}
	attempt, err := r.Store.Attempt(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return RecoveryResult{}, err
	}
	outcome, err := r.Outcomes.Inspect(ctx, task, attempt)
	if err != nil {
		return RecoveryResult{}, err
	}
	if outcome != OutcomeNone {
		return RecoveryResult{Status: RecoveryStructured, Outcome: outcome, Task: task, WorkerSession: session}, nil
	}

	if session.RecoveryPromptAt == nil {
		commandID, commandErr := newCommandID()
		if commandErr != nil {
			return RecoveryResult{}, commandErr
		}
		session, err = r.Store.ReserveRecoveryPrompt(ctx, commandID, db.ReserveRecoveryPromptInput{
			SessionID: session.ID, TaskID: task.ID, Attempt: task.CurrentAttempt,
			ExpectedVersion: session.Version, Actor: "reconciler",
		})
		if err != nil {
			return RecoveryResult{}, err
		}
		lease, leaseErr := r.Store.TreehouseLease(ctx, task.ID, task.CurrentAttempt)
		if leaseErr != nil {
			return RecoveryResult{}, leaseErr
		}
		if lease.State != domain.TreehouseLeaseActive {
			return RecoveryResult{}, db.ErrLeaseConflict
		}
		runtime := runtimeSession(session, lease.WorktreePath)
		woken, wakeErr := r.Herdr.Wake(ctx, runtime, r.recoveryPrompt())
		if wakeErr != nil {
			return RecoveryResult{}, fmt.Errorf("send recovery prompt: %w", wakeErr)
		}
		var placement *db.WorkerSessionPlacement
		if woken.WorkspaceID != runtime.WorkspaceID || woken.TabID != runtime.TabID || woken.PaneID != runtime.PaneID {
			placement = &db.WorkerSessionPlacement{
				HerdrWorkspaceID: woken.WorkspaceID, HerdrTabID: woken.TabID, HerdrPaneID: woken.PaneID,
			}
		}
		session, err = r.transitionWithPlacement(ctx, session, domain.WorkerSessionRunning, placement)
		if err != nil {
			return RecoveryResult{}, err
		}
		return RecoveryResult{Status: RecoveryPromptSent, Task: task, WorkerSession: session}, nil
	}
	if now.Sub(*session.RecoveryPromptAt) < r.recoveryWait() {
		return RecoveryResult{Status: RecoveryWaiting, Task: task, WorkerSession: session}, nil
	}
	commandID, err := newCommandID()
	if err != nil {
		return RecoveryResult{}, err
	}
	updatedTask, err := r.Store.TransitionTask(ctx, commandID, db.TransitionTaskInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State,
		ExpectedVersion: task.Version, To: domain.TaskNeedsAttention, Actor: "reconciler",
	})
	if err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{Status: RecoveryNeedsAttention, Task: updatedTask, WorkerSession: session}, nil
}

func (r *Reconciler) transition(ctx context.Context, session domain.WorkerSession, to domain.WorkerSessionState) (domain.WorkerSession, error) {
	return r.transitionWithPlacement(ctx, session, to, nil)
}

func (r *Reconciler) transitionWithPlacement(ctx context.Context, session domain.WorkerSession, to domain.WorkerSessionState, placement *db.WorkerSessionPlacement) (domain.WorkerSession, error) {
	commandID, err := newCommandID()
	if err != nil {
		return domain.WorkerSession{}, err
	}
	return r.Store.TransitionWorkerSession(ctx, commandID, db.TransitionWorkerSessionInput{
		SessionID: session.ID, TaskID: session.TaskID, Attempt: session.Attempt,
		ExpectedState: session.State, ExpectedVersion: session.Version, To: to, Actor: "reconciler",
		Placement: placement,
	})
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) stabilizationDelay() time.Duration {
	if r.StabilizationDelay > 0 {
		return r.StabilizationDelay
	}
	return taskpolicy.InFlightStabilizationWindow
}

func (r *Reconciler) recoveryWait() time.Duration {
	if r.RecoveryWait > 0 {
		return r.RecoveryWait
	}
	return 30 * time.Second
}

func (r *Reconciler) recoveryPrompt() string {
	if strings.TrimSpace(r.RecoveryPrompt) != "" {
		return r.RecoveryPrompt
	}
	return DefaultRecoveryPrompt
}
