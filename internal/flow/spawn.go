package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/herdr"
	"sophon/internal/id"
	"sophon/internal/publicsurface"
	"sophon/internal/store"
	"sophon/internal/treehouse"
	"sophon/internal/workspace"
)

// CreateMission publishes durable mission intent.
func (f *Flow) CreateMission(ctx context.Context, projectPath, title, objective string) (store.Mission, error) {
	if err := requireNonEmpty(projectPath, title, objective); err != nil {
		return store.Mission{}, fmt.Errorf("create mission: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(projectPath, workspace.MarkerName)); err == nil {
		return store.Mission{}, errors.New("workspace root is commander scope, never a mission project; select a projects/<key> child")
	} else if !errors.Is(err, os.ErrNotExist) {
		return store.Mission{}, fmt.Errorf("inspect mission project: %w", err)
	}
	release, err := store.Acquire(ctx, "mission create")
	if err != nil {
		return store.Mission{}, err
	}
	defer release()
	missionID, err := id.New("mission")
	if err != nil {
		return store.Mission{}, err
	}
	mission := store.Mission{ID: missionID, ProjectPath: projectPath, Title: title,
		Objective: objective, CreatedAt: time.Now().UTC()}
	return mission, store.CreateMission(mission)
}

// CreateWorkspaceMission resolves and pins one direct workspace child. The
// workspace organizes projects; canonical task truth remains in the store.
func (f *Flow) CreateWorkspaceMission(ctx context.Context, workspaceRoot, projectKey, title, objective string) (store.Mission, error) {
	if err := requireNonEmpty(workspaceRoot, projectKey, title, objective); err != nil {
		return store.Mission{}, fmt.Errorf("create workspace mission: %w", err)
	}
	if f.deps.Projects == nil {
		return store.Mission{}, errors.New("flow is not configured for workspace project resolution")
	}
	project, err := f.deps.Projects.Resolve(ctx, workspaceRoot, projectKey)
	if err != nil {
		return store.Mission{}, err
	}
	release, err := store.Acquire(ctx, "workspace mission create")
	if err != nil {
		return store.Mission{}, err
	}
	defer release()
	missionID, err := id.New("mission")
	if err != nil {
		return store.Mission{}, err
	}
	mission := store.Mission{ID: missionID, ProjectPath: project.Path, ProjectKey: project.Key,
		ProjectIdentity: project.Identity, WorkspaceID: project.WorkspaceID, WorkspaceRoot: project.WorkspaceRoot,
		Title: title, Objective: objective, CreatedAt: time.Now().UTC()}
	return mission, store.CreateMission(mission)
}

// CreateTask publishes durable task intent under a mission. Empty kind
// defaults to implementation; empty delivery mode defaults to local.
func (f *Flow) CreateTask(ctx context.Context, missionID, title, objective, deliveryBranch string, kind domain.TaskKind,
	mode domain.DeliveryMode, validationCommand string, reviewPosture ...domain.ReviewPosture) (store.Task, error) {
	if err := requireNonEmpty(missionID, title, objective); err != nil {
		return store.Task{}, fmt.Errorf("create task: %w", err)
	}
	if kind == "" {
		kind = domain.TaskImplementation
	}
	if mode == "" {
		if deliveryBranch == "" {
			mode = domain.DeliveryLocal
		} else {
			// Read compatibility for the original CLI contract: an explicit
			// public branch with no mode selected branch delivery.
			mode = domain.DeliveryBranch
		}
	}
	if kind != domain.TaskImplementation {
		return store.Task{}, fmt.Errorf("unknown task kind %q", kind)
	}
	switch mode {
	case domain.DeliveryLocal:
		if deliveryBranch != "" {
			return store.Task{}, errors.New("local development cannot predeclare a public delivery branch")
		}
	case domain.DeliveryBranch, domain.DeliveryPR:
		if err := publicsurface.TaskTitle(title); err != nil {
			return store.Task{}, fmt.Errorf("create task: %w", err)
		}
		if err := publicsurface.Branch(deliveryBranch); err != nil {
			return store.Task{}, fmt.Errorf("create task: %w", err)
		}
	default:
		return store.Task{}, fmt.Errorf("unknown delivery mode %q", mode)
	}
	posture := domain.ReviewOff
	if len(reviewPosture) > 0 && reviewPosture[0] != "" {
		posture = reviewPosture[0]
	}
	switch posture {
	case domain.ReviewOff, domain.ReviewOptional, domain.ReviewRequired:
	default:
		return store.Task{}, fmt.Errorf("unknown review posture %q", posture)
	}
	release, err := store.Acquire(ctx, "task create")
	if err != nil {
		return store.Task{}, err
	}
	defer release()
	taskID, err := id.New("task")
	if err != nil {
		return store.Task{}, err
	}
	task := store.Task{ID: taskID, MissionID: missionID, Title: title, Objective: objective,
		DeliveryBranch: deliveryBranch, Kind: kind, DeliveryMode: mode,
		ReviewPosture: posture, ValidationCommand: validationCommand, CreatedAt: time.Now().UTC()}
	return task, store.CreateTask(task)
}

// Spawn creates the first attempt or retries the current product revision.
// Retry never creates correction authority: a delivered revision must use
// Revise, while a correction retry reuses that revision's immutable base.
func (f *Flow) Spawn(ctx context.Context, taskID string, retry bool) (store.Spawn, error) {
	if f.deps.Git == nil || f.deps.Leases == nil || f.deps.Panes == nil {
		return store.Spawn{}, errors.New("flow is not fully configured for spawn")
	}
	release, err := store.Acquire(ctx, "spawn "+taskID)
	if err != nil {
		return store.Spawn{}, err
	}
	defer release()
	task, mission, err := f.taskAndMission(taskID)
	if err != nil {
		return store.Spawn{}, err
	}
	if _, err := store.ReadCancellation(task.MissionID, task.ID); err == nil {
		return store.Spawn{}, errors.New("cancelled task cannot be started")
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Spawn{}, err
	}
	mission, err = f.resolveMissionProject(ctx, mission)
	if err != nil {
		return store.Spawn{}, err
	}
	if task.DeliveryMode == domain.DeliveryLocal {
		if err := f.ensureBootstrapLocked(ctx, mission, task); err != nil {
			return store.Spawn{}, err
		}
	}
	reuseUnstarted := false
	if task.CurrentAttempt >= 1 {
		if _, spawnErr := store.ReadSpawn(task.MissionID, taskID, task.CurrentAttempt); errors.Is(spawnErr, store.ErrNotFound) {
			reuseUnstarted = true
		} else if spawnErr != nil {
			return store.Spawn{}, spawnErr
		} else if !retry {
			return store.Spawn{}, fmt.Errorf("%w (task %s attempt %d)", ErrAttemptsExist, taskID, task.CurrentAttempt)
		}
		if !reuseUnstarted {
			if delivery, err := store.ReadDelivery(task.MissionID, taskID, task.CurrentAttempt); err == nil && delivery.State.Terminal() {
				return store.Spawn{}, errors.New("delivered revision cannot be retried; use sophon revise for accepted open-PR feedback")
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return store.Spawn{}, err
			}
			// Fence the previous attempt's lease by exact identity, best effort: a
			// mismatch or release failure is never destructive, so continue either way.
			if previous, err := store.ReadSpawn(task.MissionID, taskID, task.CurrentAttempt); err == nil {
				f.releaseLeaseBestEffort(mission.ProjectPath, previous)
			} else if !errors.Is(err, store.ErrNotFound) {
				return store.Spawn{}, err
			}
		}
	}
	if !reuseUnstarted {
		task, err = store.AdvanceTask(task.MissionID, task.ID, false)
		if err != nil {
			return store.Spawn{}, err
		}
	}
	var correction *store.Correction
	if task.CurrentRevision > 1 {
		record, err := store.ReadCorrection(task.MissionID, task.ID, task.CurrentRevision)
		if err != nil {
			return store.Spawn{}, fmt.Errorf("retry correction revision %d: %w", task.CurrentRevision, err)
		}
		correction = &record
	}
	return f.spawnAttemptLocked(ctx, mission, task, correction)
}

func (f *Flow) resolveMissionProject(ctx context.Context, mission store.Mission) (store.Mission, error) {
	if mission.WorkspaceID == "" {
		return mission, nil
	}
	if f.deps.Projects == nil {
		return mission, errors.New("flow is not configured for workspace project resolution")
	}
	project, err := f.deps.Projects.Resolve(ctx, mission.WorkspaceRoot, mission.ProjectKey)
	if err != nil {
		return mission, fmt.Errorf("resolve mission project %s: %w", mission.ProjectKey, err)
	}
	if err := workspace.ValidatePinned(project, mission.WorkspaceID, mission.ProjectKey, mission.ProjectPath, mission.ProjectIdentity); err != nil {
		return mission, err
	}
	mission.ProjectPath = project.Path
	return mission, nil
}

func (f *Flow) ensureBootstrapLocked(ctx context.Context, mission store.Mission, task store.Task) error {
	if f.deps.Bootstrap == nil {
		return nil
	}
	state, err := f.deps.Bootstrap.InspectBootstrap(ctx, mission.ProjectPath)
	if err != nil {
		return fmt.Errorf("inspect project start baseline: %w", err)
	}
	intent, intentErr := store.ReadBootstrapIntent(task.MissionID, task.ID)
	if errors.Is(intentErr, store.ErrNotFound) && !state.Needed {
		return nil
	}
	if intentErr != nil && !errors.Is(intentErr, store.ErrNotFound) {
		return intentErr
	}
	if errors.Is(intentErr, store.ErrNotFound) {
		now := time.Now().UTC()
		intent = store.BootstrapIntent{Version: 1, TaskID: task.ID, MissionID: task.MissionID,
			ProjectKey: mission.ProjectKey, ProjectPath: mission.ProjectPath, Branch: state.Branch, Ref: state.Ref,
			CommitMessage: "Initialize project history", AuthorName: "Project Contributors",
			AuthorEmail: "contributors@localhost.invalid", AuthoredAt: now, RequestedAt: now}
		homeDir, homeErr := datahome.AbsDir()
		if homeErr != nil {
			return homeErr
		}
		if err := store.PublishImmutable(store.BootstrapIntentPath(homeDir, task.MissionID, task.ID), intent); err != nil {
			return fmt.Errorf("publish bootstrap intent: %w", err)
		}
	}
	if intent.Version != 1 || intent.TaskID != task.ID || intent.MissionID != task.MissionID ||
		intent.ProjectPath != mission.ProjectPath || intent.Branch == "" || intent.Ref == "" {
		return errors.New("bootstrap intent does not match the current task and project")
	}
	result, err := f.deps.Bootstrap.CreateBootstrap(ctx, mission.ProjectPath, gitcontrol.BootstrapSpec{
		Branch: intent.Branch, Ref: intent.Ref, CommitMessage: intent.CommitMessage,
		AuthorName: intent.AuthorName, AuthorEmail: intent.AuthorEmail, AuthoredAt: intent.AuthoredAt})
	if err != nil {
		return fmt.Errorf("create or recover empty project baseline: %w", err)
	}
	receipt := store.BootstrapReceipt{Version: 1, TaskID: task.ID, MissionID: task.MissionID,
		Branch: result.Branch, Ref: result.Ref, CommitSHA: result.CommitSHA, CompletedAt: time.Now().UTC()}
	if prior, readErr := store.ReadBootstrapReceipt(task.MissionID, task.ID); readErr == nil {
		receipt.CompletedAt = prior.CompletedAt
		if prior.CommitSHA != receipt.CommitSHA || prior.Branch != receipt.Branch || prior.Ref != receipt.Ref {
			return errors.New("bootstrap receipt conflicts with observed project baseline")
		}
	} else if !errors.Is(readErr, store.ErrNotFound) {
		return readErr
	}
	homeDir, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	return store.PublishImmutable(store.BootstrapReceiptPath(homeDir, task.MissionID, task.ID), receipt)
}

// spawnAttemptLocked performs allocation after the caller has advanced task
// identity and, for corrections, durably published immutable correction
// intent. The caller holds the shared mutation lock.
func (f *Flow) spawnAttemptLocked(ctx context.Context, mission store.Mission, task store.Task, correction *store.Correction) (store.Spawn, error) {
	attempt := task.CurrentAttempt
	allocation, err := f.deps.Leases.Acquire(ctx, mission.ProjectPath, LeaseHolder(task.ID, attempt))
	if err != nil {
		return store.Spawn{}, fmt.Errorf("acquire lease for attempt %d: %w", attempt, err)
	}
	// Compensation for every failure below: return only this exact lease.
	releaseLease := func() {
		f.deps.Leases.Release(context.Background(), mission.ProjectPath, treehouse.Allocation{
			WorktreePath: allocation.WorktreePath, LeaseID: allocation.LeaseID, LeaseHolder: allocation.LeaseHolder})
	}
	branch := TaskBranch(task.Title, task.ID, attempt)
	var snapshot gitcontrol.Snapshot
	if correction == nil {
		snapshot, err = f.deps.Git.CreateTaskBranch(ctx, allocation.WorktreePath, branch)
	} else if store.CorrectionContinuesPullRequest(*correction) {
		snapshot, err = f.deps.Git.CreateTaskBranchAt(ctx, allocation.WorktreePath, branch,
			correction.PublicBranch, correction.BaseSHA)
	} else {
		snapshot, err = f.deps.Git.CreateTaskBranchAtCommit(ctx, allocation.WorktreePath, branch, correction.BaseSHA)
	}
	if err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("create task branch: %w", err)
	}
	// Resolve the data home once to a clean absolute path. The exact value is
	// published into the brief, propagated into the worker runtime's launch
	// environment, and used for every record this spawn writes, so the worker
	// never depends on ambient environment to find the assigned store.
	homeDir, err := datahome.AbsDir()
	if err != nil {
		releaseLease()
		return store.Spawn{}, err
	}
	brief, err := f.renderBrief(homeDir, mission, task, attempt, allocation.WorktreePath, branch, snapshot.Head, correction)
	if err != nil {
		releaseLease()
		return store.Spawn{}, err
	}
	if err := store.PublishBytes(store.AttemptPath(homeDir, task.MissionID, task.ID, attempt, "brief.md"), []byte(brief)); err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("publish brief: %w", err)
	}
	parentWorkspace := f.commanderWorkspace()
	session, err := f.deps.Panes.StartCodex(ctx, herdr.StartRequest{
		TaskID: domain.TaskID(task.ID), TaskTitle: task.Title, Attempt: attempt,
		WorktreePath: allocation.WorktreePath, Brief: brief, Model: f.deps.Model,
		DataHome: homeDir, ParentWorkspace: parentWorkspace})
	if err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("start worker pane: %w", err)
	}
	spawn := store.Spawn{
		TaskID: task.ID, MissionID: task.MissionID, Attempt: attempt, Revision: task.CurrentRevision,
		WorktreePath: allocation.WorktreePath, Branch: branch, BaseSHA: snapshot.Head,
		LeaseID: allocation.LeaseID, LeaseHolder: allocation.LeaseHolder,
		Pane: session, AgentRuntime: string(session.Runtime), Model: session.Model,
		StartedAt: time.Now().UTC(),
	}
	if err := store.Publish(store.AttemptPath(homeDir, task.MissionID, task.ID, attempt, "spawn.json"), spawn); err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("publish spawn receipt: %w", err)
	}
	store.AppendWake(task.ID, fmt.Sprintf("spawned revision %d attempt %d", task.CurrentRevision, attempt))
	if parentWorkspace != "" && session.WorkspaceID != parentWorkspace {
		store.AppendWake(task.ID, "registered commander workspace unavailable; worker spawned in an isolated workspace")
	}
	return spawn, nil
}

// releaseLeaseBestEffort conditionally returns a previous attempt's lease by
// exact identity. Errors are deliberately swallowed: the identity guard in
// the Treehouse client makes a mismatched return non-destructive.
func (f *Flow) releaseLeaseBestEffort(projectPath string, spawn store.Spawn) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.deps.Leases.Release(ctx, projectPath, treehouse.Allocation{
		WorktreePath: spawn.WorktreePath, LeaseID: spawn.LeaseID, LeaseHolder: spawn.LeaseHolder})
}
