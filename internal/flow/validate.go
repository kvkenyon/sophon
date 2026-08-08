package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/store"
	"sophon/internal/validation"
)

// Validate runs the task's configured validation command in the verified
// worktree and publishes validation.json pinned to the verified head. A
// failed validation is a typed result (Passed=false), not an error.
func (f *Flow) Validate(ctx context.Context, taskID string) (store.Validation, error) {
	if f.deps.Git == nil || f.deps.NewValidator == nil {
		return store.Validation{}, errors.New("flow is not fully configured for validation")
	}
	release, err := store.Acquire(ctx, "validate "+taskID)
	if err != nil {
		return store.Validation{}, err
	}
	defer release()
	task, err := store.FindTask(taskID)
	if err != nil {
		return store.Validation{}, err
	}
	attempt, err := currentAttempt(task)
	if err != nil {
		return store.Validation{}, err
	}
	outcome, err := store.ReadOutcome(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Validation{}, fmt.Errorf("validate requires a verified outcome first: %w", err)
	}
	if strings.TrimSpace(task.ValidationCommand) == "" {
		return store.Validation{}, fmt.Errorf("task %s has no validation command configured", taskID)
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Validation{}, err
	}
	result, err := f.deps.NewValidator(task.ValidationCommand).Run(ctx, spawn.WorktreePath)
	if err != nil {
		return store.Validation{}, fmt.Errorf("run validation: %w", err)
	}
	// Fence on head drift: validation evidence is only valid for the verified head.
	snapshot, err := f.deps.Git.Snapshot(ctx, spawn.WorktreePath)
	if err != nil {
		return store.Validation{}, fmt.Errorf("snapshot worktree after validation: %w", err)
	}
	if !strings.EqualFold(snapshot.Head, outcome.HeadSHA) {
		return store.Validation{}, fmt.Errorf("%w: head moved to %s during validation", ErrHeadMismatch, snapshot.Head)
	}
	record := store.Validation{TaskID: taskID, Attempt: attempt, Command: task.ValidationCommand,
		HeadSHA: outcome.HeadSHA, ExitCode: result.ExitCode,
		Passed: result.Status == validation.Passed, RanAt: time.Now().UTC()}
	homeDir, err := datahome.Dir()
	if err != nil {
		return store.Validation{}, err
	}
	if err := store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "validation.json"), record); err != nil {
		return store.Validation{}, fmt.Errorf("publish validation receipt: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("validated: attempt %d passed=%t", attempt, record.Passed))
	return record, nil
}
