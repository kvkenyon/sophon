package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sophon/internal/id"
	"sophon/internal/store"
)

// CancelPlanned preserves accepted intent while explicitly preventing a task
// that never acquired a worker from starting. No task, attempt, or Git record
// is removed.
func (f *Flow) CancelPlanned(ctx context.Context, taskID, reason string, confirmed bool) (store.Cancellation, error) {
	if !confirmed {
		return store.Cancellation{}, errors.New("planned task cancellation requires explicit confirmation (--confirmed)")
	}
	if err := requireNonEmpty(taskID, reason); err != nil {
		return store.Cancellation{}, err
	}
	release, err := store.Acquire(ctx, "cancel planned "+taskID)
	if err != nil {
		return store.Cancellation{}, err
	}
	defer release()
	task, err := store.FindTask(taskID)
	if err != nil {
		return store.Cancellation{}, err
	}
	if existing, err := store.ReadCancellation(task.MissionID, task.ID); err == nil {
		if existing.Replacement != "" || existing.Reason != strings.TrimSpace(reason) {
			return store.Cancellation{}, errors.New("task already has a different immutable cancellation intent")
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Cancellation{}, err
	}
	if err := requireNeverSpawned(task); err != nil {
		return store.Cancellation{}, err
	}
	record := store.Cancellation{Version: 1, TaskID: task.ID, MissionID: task.MissionID,
		Reason: strings.TrimSpace(reason), CancelledAt: time.Now().UTC()}
	return record, store.CreateCancellation(record)
}

// RevisePlanned creates a replacement durable task and cancels the never-
// started predecessor with an immutable link. This avoids mutating accepted
// intent in place or pretending a worker revision exists.
func (f *Flow) RevisePlanned(ctx context.Context, taskID, title, objective, validation string, confirmed bool) (store.Task, error) {
	if !confirmed {
		return store.Task{}, errors.New("planned task revision requires explicit confirmation (--confirmed)")
	}
	if err := requireNonEmpty(taskID, title, objective); err != nil {
		return store.Task{}, err
	}
	release, err := store.Acquire(ctx, "revise planned "+taskID)
	if err != nil {
		return store.Task{}, err
	}
	defer release()
	prior, err := store.FindTask(taskID)
	if err != nil {
		return store.Task{}, err
	}
	if err := requireNeverSpawned(prior); err != nil {
		return store.Task{}, err
	}
	if cancelled, err := store.ReadCancellation(prior.MissionID, prior.ID); err == nil {
		if cancelled.Replacement == "" {
			return store.Task{}, errors.New("planned task is already cancelled without a replacement")
		}
		if cancelled.ReplacementTask == nil || cancelled.ReplacementTask.Title != strings.TrimSpace(title) ||
			cancelled.ReplacementTask.Objective != strings.TrimSpace(objective) || cancelled.ReplacementTask.ValidationCommand != validation {
			return store.Task{}, errors.New("planned task already has a different immutable replacement intent")
		}
		if replacement, readErr := store.ReadTask(prior.MissionID, cancelled.Replacement); readErr == nil {
			return replacement, nil
		} else if !errors.Is(readErr, store.ErrNotFound) {
			return store.Task{}, readErr
		}
		if cancelled.ReplacementTask == nil {
			return store.Task{}, errors.New("planned task replacement intent cannot recover the missing replacement")
		}
		if err := store.CreateTask(*cancelled.ReplacementTask); err != nil {
			return store.Task{}, fmt.Errorf("recover revised planned task: %w", err)
		}
		return *cancelled.ReplacementTask, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Task{}, err
	}
	replacementID, err := id.New("task")
	if err != nil {
		return store.Task{}, err
	}
	replacement := prior
	replacement.ID = replacementID
	replacement.Title = strings.TrimSpace(title)
	replacement.Objective = strings.TrimSpace(objective)
	replacement.ValidationCommand = validation
	replacement.CurrentAttempt = 0
	replacement.CurrentRevision = 0
	replacement.CreatedAt = time.Now().UTC()
	cancellation := store.Cancellation{Version: 1, TaskID: prior.ID, MissionID: prior.MissionID,
		Reason: "superseded by explicitly revised planned task", Replacement: replacement.ID,
		ReplacementTask: &replacement, CancelledAt: time.Now().UTC()}
	if err := store.CreateCancellation(cancellation); err != nil {
		return store.Task{}, err
	}
	if err := store.CreateTask(replacement); err != nil {
		return store.Task{}, fmt.Errorf("create revised planned task after immutable cancellation intent: %w", err)
	}
	return replacement, nil
}

func requireNeverSpawned(task store.Task) error {
	for attempt := 1; attempt <= task.CurrentAttempt; attempt++ {
		if _, err := store.ReadSpawn(task.MissionID, task.ID, attempt); err == nil {
			return errors.New("task has worker history and cannot use planned-task cancellation or revision")
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}
