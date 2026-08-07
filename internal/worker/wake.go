package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sophon/internal/db"
	"sophon/internal/domain"
	"sophon/internal/herdr"
)

var ErrWorkerUnavailable = errors.New("original worker session is unavailable")

type Waker struct {
	Store *db.Store
	Herdr herdr.Adapter
}

type WakeRequest struct {
	TaskID    domain.TaskID    `json:"task_id"`
	CommandID domain.CommandID `json:"command_id"`
	Message   string           `json:"message"`
}

// Wake resumes the current attempt's exact logical worker session and never
// changes task state. A live agent keeps its pane; a restored husk is replaced
// in the same workspace and resumes the persisted Codex session identity.
func (w *Waker) Wake(ctx context.Context, in WakeRequest) (domain.WorkerSession, error) {
	if w == nil || w.Store == nil || w.Herdr == nil {
		return domain.WorkerSession{}, errors.New("worker waker is not fully configured")
	}
	if in.TaskID == "" || in.CommandID == "" || strings.TrimSpace(in.Message) == "" {
		return domain.WorkerSession{}, errors.New("task, command ID, and wake message are required")
	}
	task, err := w.Store.Task(ctx, in.TaskID)
	if err != nil {
		return domain.WorkerSession{}, err
	}
	session, err := w.Store.WorkerSession(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return domain.WorkerSession{}, err
	}
	if session.State != domain.WorkerSessionIdle && session.State != domain.WorkerSessionInactive {
		return domain.WorkerSession{}, fmt.Errorf("%w: session is %s", ErrWorkerUnavailable, session.State)
	}
	budgetTask, err := w.Store.ReserveWorkerBudget(ctx, domain.CommandID(string(in.CommandID)+":budget:fix"), db.ReserveWorkerBudgetInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, SessionID: session.ID,
		ExpectedVersion: session.Version, Dimension: "fix_round", Actor: "commander",
	})
	if err != nil {
		return domain.WorkerSession{}, err
	}
	if budgetTask.State == domain.TaskNeedsAttention {
		return domain.WorkerSession{}, db.ErrBudgetExhausted
	}
	session, err = w.Store.WorkerSession(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return domain.WorkerSession{}, err
	}
	lease, err := w.Store.TreehouseLease(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return domain.WorkerSession{}, err
	}
	if lease.State != domain.TreehouseLeaseActive {
		return domain.WorkerSession{}, db.ErrLeaseConflict
	}
	runtime := runtimeSession(session, lease.WorktreePath)
	woken, err := w.Herdr.Wake(ctx, runtime, in.Message)
	if err != nil {
		if errors.Is(err, herdr.ErrSessionMissing) {
			_, reconcileErr := w.Store.ReconcileLostWorker(ctx, in.CommandID, db.ReconcileLostWorkerInput{
				SessionID: session.ID, TaskID: task.ID, Attempt: task.CurrentAttempt,
				ExpectedState: session.State, ExpectedVersion: session.Version, TaskVersion: task.Version,
				Reason: "expected Herdr pane is missing during wake", Actor: "commander",
			})
			if reconcileErr != nil {
				return domain.WorkerSession{}, fmt.Errorf("reconcile missing worker during wake: %w", reconcileErr)
			}
			return domain.WorkerSession{}, fmt.Errorf("%w: %s", ErrWorkerUnavailable, err)
		}
		return domain.WorkerSession{}, fmt.Errorf("wake original worker %s: %w", session.ID, err)
	}
	transition := db.TransitionWorkerSessionInput{
		SessionID: session.ID, TaskID: task.ID, Attempt: task.CurrentAttempt,
		ExpectedState: session.State, ExpectedVersion: session.Version,
		To: domain.WorkerSessionRunning, Actor: "commander",
	}
	if woken.WorkspaceID != runtime.WorkspaceID || woken.TabID != runtime.TabID || woken.PaneID != runtime.PaneID {
		transition.Placement = &db.WorkerSessionPlacement{
			HerdrWorkspaceID: woken.WorkspaceID, HerdrTabID: woken.TabID, HerdrPaneID: woken.PaneID,
		}
	}
	updated, err := w.Store.TransitionWorkerSession(ctx, in.CommandID, transition)
	if err != nil {
		return domain.WorkerSession{}, err
	}
	return updated, nil
}

func runtimeSession(session domain.WorkerSession, worktreePath ...string) herdr.Session {
	var cwd string
	if len(worktreePath) > 0 {
		cwd = worktreePath[0]
	}
	return herdr.Session{SessionName: session.HerdrSessionName, WorkspaceID: session.HerdrWorkspaceID,
		TabID: session.HerdrTabID, PaneID: session.HerdrPaneID, AgentName: session.HerdrAgentName,
		AgentSessionID: session.AgentSessionID, WorktreePath: cwd}
}
