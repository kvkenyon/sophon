package worker

import (
	"context"
	"errors"
	"fmt"
	"os"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
	"parallel-intellect/internal/id"
)

type LeaseAcquirer interface {
	Acquire(context.Context, domain.CommandID, domain.TaskID, int) (domain.TreehouseLease, error)
}

type Starter struct {
	Store      *db.Store
	Treehouse  LeaseAcquirer
	Herdr      herdr.Adapter
	Briefs     BriefGenerator
	Validation []string
	Budget     domain.WorkerBudget
}

type StartResult struct {
	Task          domain.Task           `json:"task"`
	Lease         domain.TreehouseLease `json:"lease"`
	WorkerSession domain.WorkerSession  `json:"worker_session"`
	BriefPath     string                `json:"brief_path"`
}

func (s *Starter) Start(ctx context.Context, taskID domain.TaskID) (StartResult, error) {
	if s == nil || s.Store == nil || s.Treehouse == nil || s.Herdr == nil {
		return StartResult{}, errors.New("worker starter is not fully configured")
	}
	current, err := s.Store.Task(ctx, taskID)
	if err != nil {
		return StartResult{}, err
	}
	if current.State != domain.TaskQueued {
		return StartResult{}, fmt.Errorf("start task while %s", current.State)
	}
	if current.Kind != domain.TaskImplementation || current.WorkerAgent != "codex" {
		return StartResult{}, fmt.Errorf("milestone 3 starts only implementation tasks assigned to codex")
	}
	attempt := current.CurrentAttempt
	current, err = s.transition(ctx, current, domain.TaskProvisioning)
	if err != nil {
		return StartResult{}, err
	}
	if current.State == domain.TaskNeedsAttention {
		return StartResult{Task: current}, db.ErrBudgetExhausted
	}
	leaseCommand, err := newCommandID()
	if err != nil {
		return StartResult{}, err
	}
	lease, err := s.Treehouse.Acquire(ctx, leaseCommand, taskID, attempt)
	if err != nil {
		return StartResult{}, fmt.Errorf("acquire Treehouse lease: %w", err)
	}
	current, err = s.Store.Task(ctx, taskID)
	if err != nil {
		return StartResult{}, err
	}
	current, err = s.transition(ctx, current, domain.TaskStarting)
	if err != nil {
		return StartResult{}, err
	}
	launch, err := s.Store.TaskLaunchContext(ctx, taskID)
	if err != nil {
		return StartResult{}, err
	}
	briefPath, err := s.Briefs.Render(BriefInput{
		MissionID: launch.Task.MissionID, MissionTitle: launch.MissionTitle,
		MissionObjective: launch.MissionObjective, Task: launch.Task, Attempt: attempt,
		Project: launch.ProjectName, Worktree: lease.WorktreePath, Branch: lease.Branch,
		BaseSHA: lease.BaseSHA, ValidationRequirements: s.Validation,
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("generate task brief: %w", err)
	}
	brief, err := os.ReadFile(briefPath)
	if err != nil {
		return StartResult{}, fmt.Errorf("read generated task brief: %w", err)
	}
	runtimeSession, err := s.Herdr.StartCodex(ctx, herdr.StartRequest{
		TaskID: taskID, TaskTitle: launch.Task.Title, Attempt: attempt, WorktreePath: lease.WorktreePath, Brief: string(brief),
	})
	if err != nil {
		return StartResult{}, err
	}
	sessionID, err := id.New("wsn")
	if err != nil {
		return StartResult{}, fmt.Errorf("generate worker session ID: %w", err)
	}
	commandID, err := newCommandID()
	if err != nil {
		return StartResult{}, err
	}
	current, err = s.Store.Task(ctx, taskID)
	if err != nil {
		return StartResult{}, err
	}
	if current.CurrentAttempt != attempt {
		return StartResult{}, db.ErrStaleAttempt
	}
	if current.State == domain.TaskNeedsAttention {
		return StartResult{}, launchRecoveryError(taskID)
	}
	workerSession, err := s.Store.RecordWorkerSession(ctx, commandID, db.RecordWorkerSessionInput{
		TaskID: taskID, Attempt: attempt, Actor: "scheduler",
		Session: domain.WorkerSession{ID: domain.SessionID(sessionID), Runtime: "codex",
			HerdrSessionName: runtimeSession.SessionName, HerdrWorkspaceID: runtimeSession.WorkspaceID,
			HerdrTabID: runtimeSession.TabID, HerdrPaneID: runtimeSession.PaneID,
			HerdrAgentName: runtimeSession.AgentName, AgentSessionID: runtimeSession.AgentSessionID,
			Budget: s.Budget},
	})
	if err != nil {
		var conflict *db.ConflictError
		if errors.As(err, &conflict) && conflict.Current.State == domain.TaskNeedsAttention {
			return StartResult{}, launchRecoveryError(taskID)
		}
		return StartResult{}, fmt.Errorf("record worker session: %w", err)
	}
	current, err = s.Store.Task(ctx, taskID)
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{Task: current, Lease: lease, WorkerSession: workerSession, BriefPath: briefPath}, nil
}

func launchRecoveryError(taskID domain.TaskID) error {
	return fmt.Errorf("task %s was marked needs_attention by recovery while its worker launch was in flight; run `pintellect task retry %s`, then run `pintellect task start %s` again", taskID, taskID, taskID)
}

func (s *Starter) transition(ctx context.Context, current domain.Task, to domain.TaskState) (domain.Task, error) {
	commandID, err := newCommandID()
	if err != nil {
		return domain.Task{}, err
	}
	return s.Store.TransitionTask(ctx, commandID, db.TransitionTaskInput{
		TaskID: current.ID, Attempt: current.CurrentAttempt, ExpectedState: current.State,
		ExpectedVersion: current.Version, To: to, Actor: "scheduler",
	})
}

func newCommandID() (domain.CommandID, error) {
	raw, err := id.New("cmd")
	if err != nil {
		return "", fmt.Errorf("generate command ID: %w", err)
	}
	return domain.CommandID(raw), nil
}
