package monitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sophon/internal/store"
)

type Event struct {
	Kind             string
	TaskID           string
	Attempt          int
	Phase            string
	Note             string
	Change           string
	ChangeGeneration string
}

type Forwarder interface {
	Forward(context.Context, Event) error
}

type ForwarderFunc func(context.Context, Event) error

func (fn ForwarderFunc) Forward(ctx context.Context, event Event) error { return fn(ctx, event) }

func validateCurrentAttempt(taskID string, attempt int) (store.Task, error) {
	if !safeTaskID.MatchString(taskID) || attempt < 1 {
		return store.Task{}, errors.New("task_id syntax and positive attempt are required")
	}
	task, err := store.FindTask(taskID)
	if err != nil {
		return store.Task{}, fmt.Errorf("task is not canonical: %w", err)
	}
	if task.CurrentAttempt != attempt {
		return store.Task{}, fmt.Errorf("attempt %d is stale; current attempt is %d", attempt, task.CurrentAttempt)
	}
	if _, err := store.ReadSpawn(task.MissionID, taskID, attempt); err != nil {
		return store.Task{}, fmt.Errorf("attempt has no canonical spawn receipt: %w", err)
	}
	return task, nil
}

func canonicalPath(home string, task store.Task, attempt int, change string) (string, error) {
	name := ""
	switch change {
	case ChangeCompletion:
		name = "result.json"
	case ChangeReport:
		name = "report.json"
	case ChangeVerification:
		name = "outcome.json"
	case ChangeValidation:
		name = "validation.json"
	case ChangeDelivery:
		name = "delivery.json"
	case ChangeRelease:
		name = "release.json"
	default:
		return "", fmt.Errorf("unknown task change %q", change)
	}
	return store.AttemptPath(home, task.MissionID, task.ID, attempt, name), nil
}

func CanonicalGeneration(home, taskID string, attempt int, change string) (string, error) {
	task, err := validateCurrentAttempt(taskID, attempt)
	if err != nil {
		return "", err
	}
	path, err := canonicalPath(home, task, attempt, change)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || filepath.Dir(path) == path {
		return "", errors.New("canonical notification evidence is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func validateCanonicalChange(home string, params TaskChangedParams) error {
	if !ValidChange(params.Change) || !safeGeneration.MatchString(params.ChangeGeneration) {
		return errors.New("change and 64-character change_generation are required")
	}
	task, err := validateCurrentAttempt(params.TaskID, params.Attempt)
	if err != nil {
		return err
	}
	switch params.Change {
	case ChangeCompletion:
		_, err = store.ReadResult(task.MissionID, task.ID, params.Attempt)
		if err == nil {
			if _, siblingErr := os.Stat(store.AttemptPath(home, task.MissionID, task.ID, params.Attempt, "report.json")); siblingErr == nil {
				err = errors.New("completion conflicts with a canonical report")
			} else if !errors.Is(siblingErr, os.ErrNotExist) {
				err = siblingErr
			}
		}
	case ChangeReport:
		var report store.WorkerReport
		report, err = store.ReadReport(task.MissionID, task.ID, params.Attempt)
		if err == nil && (report.TaskID != task.ID || report.Attempt != params.Attempt) {
			err = errors.New("report identity does not match the notification")
		}
		if err == nil {
			if _, siblingErr := os.Stat(store.AttemptPath(home, task.MissionID, task.ID, params.Attempt, "result.json")); siblingErr == nil {
				err = errors.New("report conflicts with canonical completion")
			} else if !errors.Is(siblingErr, os.ErrNotExist) {
				err = siblingErr
			}
		}
	case ChangeVerification:
		var outcome store.Outcome
		outcome, err = store.ReadOutcome(task.MissionID, task.ID, params.Attempt)
		if err == nil && (outcome.TaskID != task.ID || outcome.Attempt != params.Attempt ||
			outcome.HeadSHA == "" || outcome.ResultSHA256 == "" || outcome.VerifiedAt.IsZero()) {
			err = errors.New("outcome identity does not match the notification")
		}
	case ChangeValidation:
		var validation store.Validation
		validation, err = store.ReadValidation(task.MissionID, task.ID, params.Attempt)
		if err == nil && (validation.TaskID != task.ID || validation.Attempt != params.Attempt ||
			validation.Command == "" || validation.HeadSHA == "" || validation.RanAt.IsZero()) {
			err = errors.New("validation identity does not match the notification")
		}
	case ChangeDelivery:
		var delivery store.Delivery
		delivery, err = store.ReadDelivery(task.MissionID, task.ID, params.Attempt)
		if err == nil && (delivery.TaskID != task.ID || delivery.Attempt != params.Attempt || !delivery.State.Terminal() ||
			delivery.HeadSHA == "" || delivery.DeliveredAt == nil || delivery.DeliveredAt.IsZero()) {
			err = errors.New("delivery notification requires an exact terminal receipt")
		}
	case ChangeRelease:
		var release store.Release
		release, err = store.ReadRelease(task.MissionID, task.ID, params.Attempt)
		spawn, spawnErr := store.ReadSpawn(task.MissionID, task.ID, params.Attempt)
		if err == nil && spawnErr != nil {
			err = spawnErr
		}
		if err == nil && (release.TaskID != task.ID || release.Attempt != params.Attempt || release.ReleasedAt.IsZero() ||
			release.LeaseID != spawn.LeaseID || release.LeaseHolder != spawn.LeaseHolder) {
			err = errors.New("release identity does not match the notification")
		}
	}
	if err != nil {
		return fmt.Errorf("canonical %s evidence is invalid: %w", params.Change, err)
	}
	path, err := canonicalPath(home, task, params.Attempt, params.Change)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if digestBytes(data) != params.ChangeGeneration {
		return errors.New("change_generation does not match current canonical evidence")
	}
	return nil
}
