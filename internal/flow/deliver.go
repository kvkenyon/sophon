package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/delivery"
	"sophon/internal/domain"
	"sophon/internal/store"
)

// Deliver executes the operator-confirmed delivery for the current attempt.
// It publishes typed intent before any external effect and a receipt after,
// so re-running converges: a terminal receipt for the same head returns
// unchanged, and a pending intent is completed from observed reality.
func (f *Flow) Deliver(ctx context.Context, taskID string, confirmed bool) (store.Delivery, error) {
	if !confirmed {
		return store.Delivery{}, ErrNotConfirmed
	}
	if f.deps.DeliveryGit == nil || f.deps.DeliveryRemote == nil {
		return store.Delivery{}, errors.New("flow is not fully configured for delivery")
	}
	release, err := store.Acquire(ctx, "deliver "+taskID)
	if err != nil {
		return store.Delivery{}, err
	}
	defer release()
	task, err := store.FindTask(taskID)
	if err != nil {
		return store.Delivery{}, err
	}
	attempt, err := currentAttempt(task)
	if err != nil {
		return store.Delivery{}, err
	}
	outcome, err := store.ReadOutcome(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Delivery{}, fmt.Errorf("deliver requires a verified outcome first: %w", err)
	}
	if strings.TrimSpace(task.ValidationCommand) != "" {
		record, err := store.ReadValidation(task.MissionID, taskID, attempt)
		if err != nil {
			return store.Delivery{}, fmt.Errorf("deliver requires a passing validation receipt first: %w", err)
		}
		if !record.Passed || !strings.EqualFold(record.HeadSHA, outcome.HeadSHA) {
			return store.Delivery{}, fmt.Errorf("deliver requires validation passed for head %s; last receipt: passed=%t head=%s",
				outcome.HeadSHA, record.Passed, record.HeadSHA)
		}
	}
	homeDir, err := datahome.Dir()
	if err != nil {
		return store.Delivery{}, err
	}
	deliveryPath := store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "delivery.json")
	var prior *store.Delivery
	if existing, err := store.ReadDelivery(task.MissionID, taskID, attempt); err == nil {
		if !strings.EqualFold(existing.HeadSHA, outcome.HeadSHA) {
			return store.Delivery{}, fmt.Errorf("%w: recorded delivery is for head %s, verified head is %s",
				ErrHeadMismatch, existing.HeadSHA, outcome.HeadSHA)
		}
		if existing.State.Terminal() {
			// Idempotent re-run: the same attempt and head already delivered.
			return existing, nil
		}
		prior = &existing
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Delivery{}, err
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Delivery{}, err
	}
	if err := f.deps.DeliveryGit.VerifyHead(ctx, spawn.WorktreePath, spawn.Branch, outcome.HeadSHA); err != nil {
		return store.Delivery{}, err
	}
	repository, err := f.deps.DeliveryGit.Repository(ctx, spawn.WorktreePath)
	if err != nil {
		return store.Delivery{}, err
	}
	// Typed intent before any external effect; a pending intent for the same
	// head keeps its original timestamp while converging.
	intent := store.Delivery{TaskID: taskID, Attempt: attempt, Mode: task.DeliveryMode,
		Repository: repository, Branch: spawn.Branch, HeadSHA: outcome.HeadSHA,
		State: store.DeliveryPending, IntentAt: time.Now().UTC()}
	if prior != nil && prior.State == store.DeliveryPending {
		intent.IntentAt = prior.IntentAt
	}
	if err := store.Publish(deliveryPath, intent); err != nil {
		return store.Delivery{}, fmt.Errorf("publish delivery intent: %w", err)
	}
	if err := f.deps.DeliveryRemote.Push(ctx, repository, spawn.WorktreePath, spawn.Branch, outcome.HeadSHA); err != nil {
		return store.Delivery{}, fmt.Errorf("push exact head: %w", err)
	}
	remoteHead, err := f.deps.DeliveryRemote.HeadSHA(ctx, repository, spawn.WorktreePath, spawn.Branch)
	if err != nil {
		return store.Delivery{}, err
	}
	if !strings.EqualFold(remoteHead, outcome.HeadSHA) {
		return store.Delivery{}, fmt.Errorf("%w: remote branch %s is %s after push", ErrHeadMismatch, spawn.Branch, remoteHead)
	}
	now := time.Now().UTC()
	receipt := intent
	receipt.DeliveredAt = &now
	switch task.DeliveryMode {
	case domain.DeliveryBranch:
		receipt.State = store.DeliveryDeliveredBranch
	case domain.DeliveryPR:
		pr, err := f.findOrCreatePullRequest(ctx, task, attempt, repository, spawn.WorktreePath, outcome.HeadSHA)
		if err != nil {
			return store.Delivery{}, err
		}
		receipt.State = store.DeliveryDeliveredPR
		receipt.PRURL = pr.URL
		receipt.PRNumber = pr.Number
	default:
		return store.Delivery{}, fmt.Errorf("unknown delivery mode %q", task.DeliveryMode)
	}
	if err := store.Publish(deliveryPath, receipt); err != nil {
		return store.Delivery{}, fmt.Errorf("publish delivery receipt: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("delivered: attempt %d (%s)", attempt, receipt.State))
	return receipt, nil
}

// findOrCreatePullRequest resolves the PR by repository + branch + SHA,
// creating it when absent and reconciling through observed reality when the
// create races an existing PR.
func (f *Flow) findOrCreatePullRequest(ctx context.Context, task store.Task, attempt int,
	repository, worktree, headSHA string) (*delivery.PullRequest, error) {
	branch := taskBranchFor(task, attempt)
	pr, err := f.deps.DeliveryRemote.FindPullRequest(ctx, repository, worktree, branch, headSHA)
	if err != nil {
		return nil, err
	}
	if pr != nil {
		return pr, nil
	}
	created, err := f.deps.DeliveryRemote.CreatePullRequest(ctx, delivery.PullRequestInput{
		Repository: repository, Worktree: worktree, Branch: branch,
		HeadSHA: headSHA, Title: task.Title,
		Body: fmt.Sprintf("Sophon task %s attempt %d", task.ID, attempt)})
	if err == nil {
		return &created, nil
	}
	if reconciled, findErr := f.deps.DeliveryRemote.FindPullRequest(ctx, repository, worktree,
		branch, headSHA); findErr == nil && reconciled != nil {
		return reconciled, nil
	}
	return nil, fmt.Errorf("create pull request: %w", err)
}

func taskBranchFor(task store.Task, attempt int) string {
	return TaskBranch(task.Title, task.ID, attempt)
}
