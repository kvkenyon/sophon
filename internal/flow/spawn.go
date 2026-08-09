package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/herdr"
	"sophon/internal/id"
	"sophon/internal/publicsurface"
	"sophon/internal/store"
	"sophon/internal/treehouse"
)

// CreateMission publishes durable mission intent.
func (f *Flow) CreateMission(ctx context.Context, projectPath, title, objective string) (store.Mission, error) {
	if err := requireNonEmpty(projectPath, title, objective); err != nil {
		return store.Mission{}, fmt.Errorf("create mission: %w", err)
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

// CreateTask publishes durable task intent under a mission. Empty kind
// defaults to implementation; empty delivery mode defaults to branch.
func (f *Flow) CreateTask(ctx context.Context, missionID, title, objective, deliveryBranch string, kind domain.TaskKind,
	mode domain.DeliveryMode, validationCommand string, reviewPosture ...domain.ReviewPosture) (store.Task, error) {
	if err := requireNonEmpty(missionID, title, objective, deliveryBranch); err != nil {
		return store.Task{}, fmt.Errorf("create task: %w", err)
	}
	if err := publicsurface.TaskTitle(title); err != nil {
		return store.Task{}, fmt.Errorf("create task: %w", err)
	}
	if err := publicsurface.Branch(deliveryBranch); err != nil {
		return store.Task{}, fmt.Errorf("create task: %w", err)
	}
	if kind == "" {
		kind = domain.TaskImplementation
	}
	if mode == "" {
		mode = domain.DeliveryBranch
	}
	if kind != domain.TaskImplementation {
		return store.Task{}, fmt.Errorf("unknown task kind %q", kind)
	}
	switch mode {
	case domain.DeliveryBranch, domain.DeliveryPR:
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
	if task.CurrentAttempt >= 1 {
		if !retry {
			return store.Spawn{}, fmt.Errorf("%w (task %s attempt %d)", ErrAttemptsExist, taskID, task.CurrentAttempt)
		}
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
	task, err = store.AdvanceTask(task.MissionID, task.ID, false)
	if err != nil {
		return store.Spawn{}, err
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
