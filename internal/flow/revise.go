package flow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"sophon/internal/delivery"
	"sophon/internal/domain"
	"sophon/internal/herdr"
	"sophon/internal/store"
)

// Revise accepts bounded correction feedback for the same product contract,
// pins a new immutable revision to the exact current open-PR head, and starts
// its first isolated attempt. It is the sole owner of correction creation;
// Spawn --retry can only replace an attempt inside the already-created
// revision.
func (f *Flow) Revise(ctx context.Context, taskID, reason, objective string, acceptExternalHead bool) (store.Spawn, error) {
	if f.deps.Git == nil || f.deps.Leases == nil || f.deps.Panes == nil ||
		f.deps.DeliveryGit == nil || f.deps.DeliveryRemote == nil {
		return store.Spawn{}, errors.New("flow is not fully configured for correction revision")
	}
	if err := requireNonEmpty(taskID, reason, objective); err != nil {
		return store.Spawn{}, fmt.Errorf("revise: %w", err)
	}
	release, err := store.Acquire(ctx, "revise "+taskID)
	if err != nil {
		return store.Spawn{}, err
	}
	defer release()
	return f.reviseLocked(ctx, taskID, reason, objective, acceptExternalHead, nil)
}

type reviewCorrectionLink struct {
	binding   store.ReviewBinding
	sequences []int
}

// reviseLocked is the landed existing-PR revision owner. Review feedback uses
// it with a typed link so it cannot create a parallel revision lifecycle. The
// caller holds Sophon's shared mutation lock.
func (f *Flow) reviseLocked(ctx context.Context, taskID, reason, objective string, acceptExternalHead bool, review *reviewCorrectionLink) (store.Spawn, error) {
	if f.deps.Git == nil || f.deps.Leases == nil || f.deps.Panes == nil ||
		f.deps.DeliveryGit == nil || f.deps.DeliveryRemote == nil {
		return store.Spawn{}, errors.New("flow is not fully configured for correction revision")
	}
	task, mission, err := f.taskAndMission(taskID)
	if err != nil {
		return store.Spawn{}, err
	}
	mission, err = f.resolveMissionProject(ctx, mission)
	if err != nil {
		return store.Spawn{}, err
	}
	effectiveTask, _, deliveryErr := effectiveDeliveryTask(task)
	if deliveryErr != nil {
		return store.Spawn{}, deliveryErr
	}
	task = effectiveTask
	if review != nil {
		if err := validateReviewCorrectionLink(task, *review); err != nil {
			return store.Spawn{}, err
		}
	}
	if task.DeliveryMode != domain.DeliveryPR {
		return store.Spawn{}, errors.New("correction revisions require an existing pull-request delivery")
	}
	priorAttempt, err := currentAttempt(task)
	if err != nil {
		return store.Spawn{}, err
	}
	priorDelivery, err := store.ReadDelivery(task.MissionID, taskID, priorAttempt)
	if err != nil || priorDelivery.State != store.DeliveryDeliveredPR {
		if err == nil {
			err = errors.New("current revision is not a delivered pull request")
		}
		return store.Spawn{}, fmt.Errorf("cannot create correction over unlanded revision: %w", err)
	}
	if priorDelivery.PRNumber < 1 || priorDelivery.PRURL == "" || priorDelivery.Repository == "" ||
		priorDelivery.Branch != task.DeliveryBranch {
		return store.Spawn{}, fmt.Errorf("%w: prior delivery lacks complete canonical PR identity", ErrReconciliation)
	}
	priorSpawn, err := store.ReadSpawn(task.MissionID, taskID, priorAttempt)
	if err != nil {
		return store.Spawn{}, err
	}
	// An unreleased prior copy must still be clean, exact, and retired. A
	// released copy is no longer inspected because Treehouse may reuse it.
	if _, err := store.ReadRelease(task.MissionID, taskID, priorAttempt); errors.Is(err, store.ErrNotFound) {
		if err := f.deps.DeliveryGit.VerifyHead(ctx, priorSpawn.WorktreePath, priorSpawn.Branch, priorDelivery.HeadSHA); err != nil {
			return store.Spawn{}, fmt.Errorf("dirty or unresolved delivered copy blocks correction: %w", err)
		}
		state, err := f.deps.Panes.Observe(ctx, priorSpawn.Pane)
		if err != nil {
			return store.Spawn{}, fmt.Errorf("cannot prove delivered worker is retired: %w", err)
		}
		if state != herdr.StateLost {
			return store.Spawn{}, fmt.Errorf("live delivered worker (%s) blocks correction creation", state)
		}
	} else if err != nil {
		return store.Spawn{}, err
	}

	repository, err := f.deps.DeliveryGit.Repository(ctx, mission.ProjectPath)
	if err != nil {
		return store.Spawn{}, err
	}
	if repository != priorDelivery.Repository {
		return store.Spawn{}, fmt.Errorf("%w: repository changed from %s to %s", ErrReconciliation, priorDelivery.Repository, repository)
	}
	pr, remoteHead, err := f.observeExactPR(ctx, mission.ProjectPath, priorDelivery)
	if err != nil {
		return store.Spawn{}, err
	}
	switch pr.State {
	case delivery.PullRequestMerged:
		return store.Spawn{}, errors.New("merged pull request is terminal; create new work")
	case delivery.PullRequestClosed:
		return store.Spawn{}, errors.New("closed-unmerged pull request requires an explicit operator decision to reopen it or create replacement work")
	case delivery.PullRequestOpen:
	default:
		return store.Spawn{}, fmt.Errorf("%w: unknown pull request state %q", ErrReconciliation, pr.State)
	}
	baseSHA := priorDelivery.HeadSHA
	if !strings.EqualFold(remoteHead, priorDelivery.HeadSHA) {
		if !acceptExternalHead {
			return store.Spawn{}, fmt.Errorf("%w: public head advanced from %s to %s; re-run revise with --accept-external-head only after review",
				ErrReconciliation, priorDelivery.HeadSHA, remoteHead)
		}
		if err := f.deps.DeliveryGit.FetchBranch(ctx, mission.ProjectPath, task.DeliveryBranch, remoteHead); err != nil {
			return store.Spawn{}, fmt.Errorf("reconcile external public head: %w", err)
		}
		if err := f.deps.DeliveryGit.VerifyStrictDescendant(ctx, mission.ProjectPath, priorDelivery.HeadSHA, remoteHead); err != nil {
			return store.Spawn{}, fmt.Errorf("non-fast-forward public history cannot become a correction base: %w", err)
		}
		baseSHA = remoteHead
		// Re-read immediately before publishing intent: anything that changed
		// during reconciliation is drift, not silently accepted authority.
		pr, remoteHead, err = f.observeExactPR(ctx, mission.ProjectPath, priorDelivery)
		if err != nil {
			return store.Spawn{}, err
		}
		if pr.State != delivery.PullRequestOpen || !strings.EqualFold(remoteHead, baseSHA) {
			return store.Spawn{}, fmt.Errorf("%w: pull request changed during correction intake", ErrReconciliation)
		}
	}

	priorRevision := store.CurrentRevision(task)
	correction := store.Correction{
		Version: 1, TaskID: task.ID, MissionID: task.MissionID, Revision: priorRevision + 1,
		PriorRevision: priorRevision, PriorAttempt: priorAttempt, Reason: strings.TrimSpace(reason),
		Objective: strings.TrimSpace(objective), Repository: repository, PublicBranch: task.DeliveryBranch,
		PRURL: pr.URL, PRNumber: pr.Number, BaseRepository: pr.BaseRepository,
		BaseBranch: pr.BaseBranch, BaseSHA: strings.ToLower(baseSHA), AcceptedAt: time.Now().UTC(),
	}
	if review != nil {
		correction.Source = store.CorrectionSourceReadCode
		correction.ReviewAttempt = review.binding.Attempt
		correction.ReviewSession = review.binding.SessionID
		correction.ReviewFeedback = append([]int(nil), review.sequences...)
	}
	if existing, readErr := store.ReadCorrection(task.MissionID, task.ID, correction.Revision); readErr == nil {
		// Crash recovery for the only pre-pointer window: an exact immutable
		// intent may already exist even though task.json still names the prior
		// delivered revision. Reuse it; any differing intent blocks duplication.
		correction.AcceptedAt = existing.AcceptedAt
		if !reflect.DeepEqual(existing, correction) {
			return store.Spawn{}, errors.New("pending correction intent differs from this correction request; reconcile before creating another revision")
		}
		correction = existing
	} else if !errors.Is(readErr, store.ErrNotFound) {
		return store.Spawn{}, readErr
	} else if err := store.CreateCorrection(correction); err != nil {
		return store.Spawn{}, fmt.Errorf("publish correction intent: %w", err)
	}
	if review != nil {
		if err := publishReviewCorrectionRoutes(task, review.binding, review.sequences,
			correction.Revision, priorAttempt+1, correction.AcceptedAt); err != nil {
			return store.Spawn{}, err
		}
	}
	// The immutable correction intent is durable before task identity advances
	// and before any Treehouse, Git, or Herdr allocation effect.
	task, err = store.AdvanceTask(task.MissionID, task.ID, true)
	if err != nil {
		return store.Spawn{}, err
	}
	if task.CurrentRevision != correction.Revision || task.CurrentAttempt != priorAttempt+1 {
		return store.Spawn{}, fmt.Errorf("%w: task pointer did not advance to correction revision %d attempt %d",
			ErrEvidenceConflict, correction.Revision, priorAttempt+1)
	}
	spawn, err := f.spawnAttemptLocked(ctx, mission, task, &correction)
	if err != nil {
		return store.Spawn{}, err
	}
	store.AppendWake(taskID, fmt.Sprintf("correction revision %d based at %s", correction.Revision, correction.BaseSHA))
	return spawn, nil
}

// observeExactPR checks immutable delivery identity plus the current remote
// branch/head. Legacy delivery receipts may omit base identity; intake/status
// accept the forge's canonical base only for that compatibility case, and a
// new correction record pins it thereafter. The helper performs no writes and
// is shared by intake, status, and correction delivery.
func (f *Flow) observeExactPR(ctx context.Context, repositoryPath string, expected store.Delivery) (delivery.PullRequest, string, error) {
	pr, err := f.deps.DeliveryRemote.ObservePullRequest(ctx, expected.Repository, expected.PRNumber)
	if err != nil {
		return delivery.PullRequest{}, "", fmt.Errorf("%w: observe pull request: %v", ErrReconciliation, err)
	}
	if pr.Repository != expected.Repository || pr.Branch != expected.Branch || pr.Number != expected.PRNumber ||
		pr.URL != expected.PRURL ||
		(expected.BaseRepository != "" && pr.BaseRepository != expected.BaseRepository) ||
		(expected.BaseBranch != "" && pr.BaseBranch != expected.BaseBranch) {
		return delivery.PullRequest{}, "", fmt.Errorf("%w: pull request repository/base/branch/number identity changed", ErrReconciliation)
	}
	head, exists, err := f.deps.DeliveryRemote.BranchHead(ctx, expected.Repository, repositoryPath, expected.Branch)
	if err != nil {
		return delivery.PullRequest{}, "", fmt.Errorf("%w: inspect public branch: %v", ErrReconciliation, err)
	}
	if !exists {
		if pr.State == delivery.PullRequestMerged {
			return pr, pr.HeadSHA, nil
		}
		return delivery.PullRequest{}, "", fmt.Errorf("%w: public branch was deleted", ErrReconciliation)
	}
	if !strings.EqualFold(head, pr.HeadSHA) {
		return delivery.PullRequest{}, "", fmt.Errorf("%w: forge head %s differs from remote branch %s", ErrReconciliation, pr.HeadSHA, head)
	}
	return pr, strings.ToLower(head), nil
}
