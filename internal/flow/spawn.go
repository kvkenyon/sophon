package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	"sophon/internal/herdr"
	"sophon/internal/id"
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
func (f *Flow) CreateTask(ctx context.Context, missionID, title string, kind domain.TaskKind,
	mode domain.DeliveryMode, validationCommand string) (store.Task, error) {
	if err := requireNonEmpty(missionID, title); err != nil {
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
	release, err := store.Acquire(ctx, "task create")
	if err != nil {
		return store.Task{}, err
	}
	defer release()
	taskID, err := id.New("task")
	if err != nil {
		return store.Task{}, err
	}
	task := store.Task{ID: taskID, MissionID: missionID, Title: title, Kind: kind,
		DeliveryMode: mode, ValidationCommand: validationCommand, CreatedAt: time.Now().UTC()}
	return task, store.CreateTask(task)
}

// Spawn fences (on retry), bumps the current-attempt token, acquires the
// lease, creates the task branch, publishes the brief, starts the worker
// pane, and only then publishes spawn.json. Any failure after lease
// acquisition conditionally releases that lease best-effort.
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
		// Fence the previous attempt's lease by exact identity, best effort: a
		// mismatch or release failure is never destructive, so continue either way.
		if previous, err := store.ReadSpawn(task.MissionID, taskID, task.CurrentAttempt); err == nil {
			f.releaseLeaseBestEffort(mission.ProjectPath, previous)
		} else if !errors.Is(err, store.ErrNotFound) {
			return store.Spawn{}, err
		}
	}
	task, err = store.BumpAttempt(task.MissionID, task.ID)
	if err != nil {
		return store.Spawn{}, err
	}
	attempt := task.CurrentAttempt
	allocation, err := f.deps.Leases.Acquire(ctx, mission.ProjectPath, LeaseHolder(taskID, attempt))
	if err != nil {
		return store.Spawn{}, fmt.Errorf("acquire lease for attempt %d: %w", attempt, err)
	}
	// Compensation for every failure below: return only this exact lease.
	releaseLease := func() {
		f.deps.Leases.Release(context.Background(), mission.ProjectPath, treehouse.Allocation{
			WorktreePath: allocation.WorktreePath, LeaseID: allocation.LeaseID, LeaseHolder: allocation.LeaseHolder})
	}
	branch := TaskBranch(task.Title, taskID, attempt)
	snapshot, err := f.deps.Git.CreateTaskBranch(ctx, allocation.WorktreePath, branch)
	if err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("create task branch: %w", err)
	}
	brief, err := f.renderBrief(mission, task, attempt, allocation.WorktreePath, branch, snapshot.Head)
	if err != nil {
		releaseLease()
		return store.Spawn{}, err
	}
	homeDir, err := datahome.Dir()
	if err != nil {
		releaseLease()
		return store.Spawn{}, err
	}
	if err := store.PublishBytes(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "brief.md"), []byte(brief)); err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("publish brief: %w", err)
	}
	session, err := f.deps.Panes.StartCodex(ctx, herdr.StartRequest{
		TaskID: domain.TaskID(taskID), TaskTitle: task.Title, Attempt: attempt,
		WorktreePath: allocation.WorktreePath, Brief: brief, Model: f.deps.Model})
	if err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("start worker pane: %w", err)
	}
	spawn := store.Spawn{
		TaskID: taskID, MissionID: task.MissionID, Attempt: attempt,
		WorktreePath: allocation.WorktreePath, Branch: branch, BaseSHA: snapshot.Head,
		LeaseID: allocation.LeaseID, LeaseHolder: allocation.LeaseHolder,
		Pane: session, AgentRuntime: string(session.Runtime), Model: session.Model,
		StartedAt: time.Now().UTC(),
	}
	if err := store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "spawn.json"), spawn); err != nil {
		releaseLease()
		return store.Spawn{}, fmt.Errorf("publish spawn receipt: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("spawned attempt %d", attempt))
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
