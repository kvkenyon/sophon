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
	"sophon/internal/publicsurface"
	"sophon/internal/store"
)

// Deliver executes one explicitly confirmed delivery effect for the current
// attempt/revision. First delivery creates the public surface; correction
// delivery only fast-forwards the exact existing open PR branch.
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
	if outcome.TaskID != taskID || outcome.Attempt != attempt ||
		(outcome.Revision != 0 && outcome.Revision != spawn.Revision) {
		return store.Delivery{}, fmt.Errorf("%w: outcome identity does not match current revision", ErrEvidenceConflict)
	}
	revision := spawn.Revision
	if revision == 0 {
		revision = store.CurrentRevision(task)
	}
	correction, correctionErr := store.ReadCorrection(task.MissionID, taskID, revision)
	if correctionErr != nil && !errors.Is(correctionErr, store.ErrNotFound) {
		return store.Delivery{}, correctionErr
	}
	if err := f.deps.DeliveryGit.VerifyHead(ctx, spawn.WorktreePath, spawn.Branch, outcome.HeadSHA); err != nil {
		return store.Delivery{}, err
	}
	result, err := store.ReadResult(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Delivery{}, fmt.Errorf("read verified result for public delivery: %w", err)
	}
	body := publicsurface.PullRequestBody(task.Title, result)
	commitMessages, err := f.deps.DeliveryGit.CommitMessages(ctx, spawn.WorktreePath, spawn.BaseSHA, outcome.HeadSHA)
	if err != nil {
		return store.Delivery{}, err
	}
	preflight := publicsurface.Preflight
	if correctionErr == nil {
		preflight = publicsurface.PreflightExistingBranch
	}
	if err := preflight(task.DeliveryBranch, task.Title, body, commitMessages); err != nil {
		return store.Delivery{}, fmt.Errorf("public delivery preflight refused: %w", err)
	}
	if prior != nil && prior.State == store.DeliveryPending &&
		(prior.Branch != task.DeliveryBranch || prior.Mode != task.DeliveryMode) {
		return store.Delivery{}, errors.New("pending delivery intent does not match current public branch and mode")
	}
	repository, err := f.deps.DeliveryGit.Repository(ctx, spawn.WorktreePath)
	if err != nil {
		return store.Delivery{}, err
	}
	if prior != nil && prior.State == store.DeliveryPending && prior.Repository != repository {
		return store.Delivery{}, errors.New("pending delivery intent does not match current repository")
	}
	if prior != nil && prior.State == store.DeliveryPending &&
		(prior.TaskID != task.ID || prior.Attempt != attempt ||
			(prior.Revision != 0 && prior.Revision != revision)) {
		return store.Delivery{}, errors.New("pending delivery intent does not match current revision and attempt")
	}
	var receipt store.Delivery
	if correctionErr == nil {
		receipt, err = f.deliverCorrection(ctx, task, spawn, outcome, correction, prior, repository, deliveryPath)
	} else {
		receipt, err = f.deliverFirst(ctx, task, spawn, outcome, prior, repository, body, deliveryPath)
	}
	if err != nil {
		return store.Delivery{}, err
	}
	if err := store.Publish(deliveryPath, receipt); err != nil {
		return store.Delivery{}, fmt.Errorf("publish delivery receipt: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("delivered: revision %d attempt %d (%s)", revision, attempt, receipt.State))
	return receipt, nil
}

func (f *Flow) deliverFirst(ctx context.Context, task store.Task, spawn store.Spawn, outcome store.Outcome,
	prior *store.Delivery, repository, body, deliveryPath string) (store.Delivery, error) {
	remoteHead, branchExists, err := f.deps.DeliveryRemote.BranchHead(ctx, repository, spawn.WorktreePath, task.DeliveryBranch)
	if err != nil {
		return store.Delivery{}, err
	}
	if branchExists && !strings.EqualFold(remoteHead, outcome.HeadSHA) {
		return store.Delivery{}, fmt.Errorf("public delivery branch %q already exists at a different head", task.DeliveryBranch)
	}
	baseBranch := ""
	if task.DeliveryMode == domain.DeliveryPR {
		if prior != nil && prior.BaseBranch != "" {
			baseBranch = prior.BaseBranch
		} else {
			baseBranch, err = f.deps.DeliveryRemote.DefaultBranch(ctx, repository, spawn.WorktreePath)
			if err != nil {
				return store.Delivery{}, err
			}
		}
	}
	intent := store.Delivery{TaskID: task.ID, Attempt: spawn.Attempt, Revision: spawn.Revision, Mode: task.DeliveryMode,
		Repository: repository, Branch: task.DeliveryBranch, HeadSHA: outcome.HeadSHA,
		BaseRepository: repository, BaseBranch: baseBranch, State: store.DeliveryPending, IntentAt: time.Now().UTC()}
	if prior != nil && prior.State == store.DeliveryPending {
		intent.IntentAt = prior.IntentAt
		if prior.BaseRepository != "" {
			intent.BaseRepository = prior.BaseRepository
		}
	}
	if err := store.Publish(deliveryPath, intent); err != nil {
		return store.Delivery{}, fmt.Errorf("publish delivery intent: %w", err)
	}
	if !branchExists {
		if err := f.deps.DeliveryRemote.Push(ctx, repository, spawn.WorktreePath, task.DeliveryBranch, outcome.HeadSHA); err != nil {
			return store.Delivery{}, fmt.Errorf("push exact head: %w", err)
		}
	}
	remoteHead, err = f.deps.DeliveryRemote.HeadSHA(ctx, repository, spawn.WorktreePath, task.DeliveryBranch)
	if err != nil {
		return store.Delivery{}, err
	}
	if !strings.EqualFold(remoteHead, outcome.HeadSHA) {
		return store.Delivery{}, fmt.Errorf("%w: remote branch %s is %s after push", ErrHeadMismatch, task.DeliveryBranch, remoteHead)
	}
	now := time.Now().UTC()
	receipt := intent
	receipt.DeliveredAt = &now
	switch task.DeliveryMode {
	case domain.DeliveryBranch:
		receipt.State = store.DeliveryDeliveredBranch
	case domain.DeliveryPR:
		pr, err := f.findOrCreatePullRequest(ctx, task, repository, spawn.WorktreePath, outcome.HeadSHA, body, baseBranch)
		if err != nil {
			return store.Delivery{}, err
		}
		if pr.Repository != repository || pr.Branch != task.DeliveryBranch || pr.BaseRepository != repository ||
			pr.BaseBranch != baseBranch || !strings.EqualFold(pr.HeadSHA, outcome.HeadSHA) {
			return store.Delivery{}, fmt.Errorf("%w: created pull request identity does not match delivery intent", ErrReconciliation)
		}
		receipt.State = store.DeliveryDeliveredPR
		receipt.PRURL = pr.URL
		receipt.PRNumber = pr.Number
	default:
		return store.Delivery{}, fmt.Errorf("unknown delivery mode %q", task.DeliveryMode)
	}
	return receipt, nil
}

func (f *Flow) deliverCorrection(ctx context.Context, task store.Task, spawn store.Spawn, outcome store.Outcome,
	correction store.Correction, prior *store.Delivery, repository, deliveryPath string) (store.Delivery, error) {
	if task.DeliveryMode != domain.DeliveryPR || repository != correction.Repository ||
		spawn.Revision != correction.Revision || !strings.EqualFold(spawn.BaseSHA, correction.BaseSHA) {
		return store.Delivery{}, fmt.Errorf("%w: correction spawn does not match immutable correction intent", ErrReconciliation)
	}
	if err := f.deps.DeliveryGit.VerifyStrictDescendant(ctx, spawn.WorktreePath, correction.BaseSHA, outcome.HeadSHA); err != nil {
		return store.Delivery{}, fmt.Errorf("correction head must strictly descend from exact PR head: %w", err)
	}
	intent := store.Delivery{TaskID: task.ID, Attempt: spawn.Attempt, Revision: correction.Revision, Mode: domain.DeliveryPR,
		Repository: repository, Branch: correction.PublicBranch, HeadSHA: outcome.HeadSHA, PriorHeadSHA: correction.BaseSHA,
		BaseRepository: correction.BaseRepository, BaseBranch: correction.BaseBranch,
		PRURL: correction.PRURL, PRNumber: correction.PRNumber,
		State: store.DeliveryPending, IntentAt: time.Now().UTC()}
	if prior != nil && prior.State == store.DeliveryPending {
		if prior.PriorHeadSHA != intent.PriorHeadSHA || prior.BaseRepository != intent.BaseRepository ||
			prior.BaseBranch != intent.BaseBranch || prior.PRURL != intent.PRURL || prior.PRNumber != intent.PRNumber {
			return store.Delivery{}, errors.New("pending correction delivery intent does not match immutable PR identity")
		}
		intent.IntentAt = prior.IntentAt
	}
	if err := store.Publish(deliveryPath, intent); err != nil {
		return store.Delivery{}, fmt.Errorf("publish correction delivery intent: %w", err)
	}
	expected := store.Delivery{Repository: correction.Repository, Branch: correction.PublicBranch,
		HeadSHA: correction.BaseSHA, BaseRepository: correction.BaseRepository, BaseBranch: correction.BaseBranch,
		PRURL: correction.PRURL, PRNumber: correction.PRNumber, State: store.DeliveryDeliveredPR}
	pr, remoteHead, err := f.observeExactPR(ctx, spawn.WorktreePath, expected)
	if err != nil {
		return store.Delivery{}, err
	}
	if pr.State != delivery.PullRequestOpen {
		if pr.State == delivery.PullRequestMerged {
			return store.Delivery{}, errors.New("pull request merged before correction delivery; no push performed")
		}
		return store.Delivery{}, errors.New("pull request closed before correction delivery; operator reconciliation required")
	}
	switch {
	case strings.EqualFold(remoteHead, correction.BaseSHA):
		if err := f.deps.DeliveryRemote.PushFastForward(ctx, repository, spawn.WorktreePath,
			correction.PublicBranch, correction.BaseSHA, outcome.HeadSHA); err != nil {
			return store.Delivery{}, fmt.Errorf("fast-forward existing pull request branch: %w", err)
		}
	case prior != nil && prior.State == store.DeliveryPending && strings.EqualFold(remoteHead, outcome.HeadSHA):
		// Crash recovery: the ordinary fast-forward already landed.
	default:
		return store.Delivery{}, fmt.Errorf("%w: correction base %s, current public head %s", ErrReconciliation, correction.BaseSHA, remoteHead)
	}
	pr, remoteHead, err = f.observeExactPR(ctx, spawn.WorktreePath, expected)
	if err != nil {
		return store.Delivery{}, err
	}
	if pr.State != delivery.PullRequestOpen || !strings.EqualFold(remoteHead, outcome.HeadSHA) {
		return store.Delivery{}, fmt.Errorf("%w: existing pull request did not converge to correction head", ErrReconciliation)
	}
	now := time.Now().UTC()
	receipt := intent
	receipt.State = store.DeliveryDeliveredPR
	receipt.DeliveredAt = &now
	return receipt, nil
}

// findOrCreatePullRequest resolves the PR by repository + branch + SHA,
// creating it when absent and reconciling through observed reality when the
// create races an existing PR.
func (f *Flow) findOrCreatePullRequest(ctx context.Context, task store.Task,
	repository, worktree, headSHA, body, baseBranch string) (*delivery.PullRequest, error) {
	branch := task.DeliveryBranch
	pr, err := f.deps.DeliveryRemote.FindPullRequest(ctx, repository, worktree, branch, headSHA)
	if err != nil {
		return nil, err
	}
	if pr != nil {
		return pr, nil
	}
	created, err := f.deps.DeliveryRemote.CreatePullRequest(ctx, delivery.PullRequestInput{
		Repository: repository, Worktree: worktree, Branch: branch,
		HeadSHA: headSHA, Base: baseBranch, Title: task.Title, Body: body})
	if err == nil {
		observed, observeErr := f.deps.DeliveryRemote.ObservePullRequest(ctx, repository, created.Number)
		if observeErr != nil {
			return nil, observeErr
		}
		return &observed, nil
	}
	if reconciled, findErr := f.deps.DeliveryRemote.FindPullRequest(ctx, repository, worktree,
		branch, headSHA); findErr == nil && reconciled != nil {
		return reconciled, nil
	}
	return nil, fmt.Errorf("create pull request: %w", err)
}
