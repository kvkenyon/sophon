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
	"sophon/internal/reviewbridge"
	"sophon/internal/store"
	"sophon/internal/treehouse"
)

// ReleaseLease conditionally returns the current attempt's lease by exact
// recorded identity and publishes release.json. Re-running returns the
// existing receipt without touching Treehouse.
func (f *Flow) ReleaseLease(ctx context.Context, taskID string) (store.Release, error) {
	return f.ReleaseLeaseAttempt(ctx, taskID, 0)
}

// ReleaseLeaseAttempt retires one exact immutable attempt copy. attempt zero
// selects the current attempt; an explicit historical attempt lets revisions
// be cleaned up independently without changing task/PR continuation truth.
func (f *Flow) ReleaseLeaseAttempt(ctx context.Context, taskID string, requestedAttempt int) (store.Release, error) {
	if f.deps.Leases == nil {
		return store.Release{}, errors.New("flow is not fully configured for lease release")
	}
	release, err := store.Acquire(ctx, "release "+taskID)
	if err != nil {
		return store.Release{}, err
	}
	defer release()
	task, mission, err := f.taskAndMission(taskID)
	if err != nil {
		return store.Release{}, err
	}
	attempt := requestedAttempt
	if attempt == 0 {
		attempt, err = currentAttempt(task)
		if err != nil {
			return store.Release{}, err
		}
	} else if attempt < 1 || attempt > task.CurrentAttempt {
		return store.Release{}, fmt.Errorf("release attempt %d is outside task history", attempt)
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Release{}, err
	}
	if existing, err := store.ReadRelease(task.MissionID, taskID, attempt); err == nil {
		if existing.TaskID != taskID || existing.Attempt != attempt || (existing.Revision != 0 && existing.Revision != spawn.Revision) || existing.LeaseID != spawn.LeaseID ||
			existing.LeaseHolder != spawn.LeaseHolder || existing.ReleasedAt.IsZero() {
			return store.Release{}, fmt.Errorf("%w: existing release receipt does not match current spawn identity", ErrEvidenceConflict)
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Release{}, err
	}
	// The release is conditional on exact lease id and holder; any failure is
	// a fence and publishes nothing.
	if err := f.deps.Leases.Release(ctx, mission.ProjectPath, treehouse.Allocation{
		WorktreePath: spawn.WorktreePath, LeaseID: spawn.LeaseID, LeaseHolder: spawn.LeaseHolder}); err != nil {
		return store.Release{}, fmt.Errorf("release lease %s/%s for attempt %d: %w",
			spawn.LeaseID, spawn.LeaseHolder, attempt, err)
	}
	record := store.Release{TaskID: taskID, Attempt: attempt, Revision: spawn.Revision, LeaseID: spawn.LeaseID,
		LeaseHolder: spawn.LeaseHolder, ReleasedAt: time.Now().UTC()}
	homeDir, err := datahome.Dir()
	if err != nil {
		return store.Release{}, err
	}
	if err := store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "release.json"), record); err != nil {
		return store.Release{}, fmt.Errorf("publish release receipt: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("released: attempt %d lease returned", attempt))
	return record, nil
}

// MissionStatus rolls one mission up with its derived task states.
type MissionStatus struct {
	Mission store.Mission      `json:"mission"`
	Tasks   []store.TaskStatus `json:"tasks"`
}

// Action kinds a commander can execute deterministically. Lifecycle actions
// derive from records; review-reconcile may additionally derive from a
// missing volatile bridge, whose absence costs latency but no truth.
const (
	ActionVerifyComplete  = "verify-complete"
	ActionValidate        = "validate"
	ActionOpenReview      = "open-review"
	ActionReadReview      = "read-review-feedback"
	ActionApplyReview     = "apply-review-feedback"
	ActionReviewApproved  = "review-approved"
	ActionReviewReconcile = "review-reconcile"
)

// Action is one currently authorized deterministic commander action with the
// exact command that performs it. The list is an action queue: a commander
// drains every entry, re-derives, and repeats until none remain before it
// reports or waits.
type Action struct {
	TaskID  string `json:"task_id"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
}

// Report is the full read-time status across every mission plus the derived
// action queue. Every action is an exact bounded command, never prose truth.
type Report struct {
	Missions []MissionStatus `json:"missions"`
	Actions  []Action        `json:"actions"`
}

// Status derives every task's state from records and augments active tasks
// with live pane observation. It takes no lock and never reads wake lines.
// It also derives the commander action queue: every ready task yields an
// exact verify-complete action and every verified task whose configured
// validation has no receipt yet yields an exact validate action — verify
// actions first, then validate actions. An existing validation receipt
// (pass or fail) is terminal for the queue: a failure needs commander
// judgment (correction routing), never a blind re-run. By default released
// tasks and released-only missions are filtered; includeReleased=true selects
// immutable history. Attention and invalid task evidence yield no lifecycle
// action; invalid review evidence may yield only an exact reconcile action.
func (f *Flow) Status(ctx context.Context, includeReleased ...bool) (Report, error) {
	showAll := len(includeReleased) > 0 && includeReleased[0]
	homeDir, err := datahome.AbsDir()
	if err != nil {
		return Report{}, err
	}
	missions, err := store.ListMissions()
	if err != nil {
		return Report{}, err
	}
	report := Report{Missions: make([]MissionStatus, 0, len(missions))}
	var verify, validate, reviews []Action
	for _, mission := range missions {
		entry := MissionStatus{Mission: mission}
		tasks, err := store.ListTasks(mission.ID)
		if err != nil {
			return Report{}, err
		}
		for _, task := range tasks {
			status, err := store.Derive(task)
			if err != nil {
				return Report{}, err
			}
			if status.State == store.StateActive {
				status = f.augmentActive(ctx, status)
			} else if status.State == store.StateCorrectionActive {
				live := status
				live.State = store.StateActive
				live = f.augmentActive(ctx, live)
				status.Detail = "worker=" + live.State
			}
			if task.DeliveryMode == domain.DeliveryPR {
				status = f.augmentPullRequest(ctx, mission, status)
			}
			if showAll {
				status.Revisions, err = store.DeriveHistory(task)
				if err != nil {
					return Report{}, err
				}
			}
			status.Review = DeriveReview(task)
			if status.Review.SessionID != "" && !status.Review.Ended {
				if binding, bindingErr := store.ReadReviewBinding(task.MissionID, task.ID, status.Review.Attempt); bindingErr == nil {
					status.Review.BridgeRunning, _ = reviewbridge.Running(homeDir, reviewbridge.Expected(homeDir, binding))
				}
			}
			if showAll {
				for attempt := 1; attempt <= task.CurrentAttempt; attempt++ {
					if _, err := store.ReadReviewBinding(task.MissionID, task.ID, attempt); err == nil {
						status.ReviewHistory = append(status.ReviewHistory, deriveReviewAttempt(task, attempt))
					} else if !errors.Is(err, store.ErrNotFound) {
						status.ReviewHistory = append(status.ReviewHistory, store.ReviewStatus{Version: 1,
							TaskID: task.ID, Attempt: attempt, State: "invalid-evidence",
							Detail: "historical review binding evidence is invalid"})
					}
				}
			}
			if status.State == store.StateReleased && !showAll {
				continue
			}
			entry.Tasks = append(entry.Tasks, status)
			switch {
			case status.State == store.StateReady || status.State == store.StateCorrectionReady:
				verify = append(verify, Action{TaskID: task.ID, Kind: ActionVerifyComplete,
					Command: "sophon verify-complete " + task.ID})
			case (status.State == store.StateVerified || status.State == store.StateCorrectionVerified) && strings.TrimSpace(task.ValidationCommand) != "":
				if _, err := store.ReadValidation(task.MissionID, task.ID, status.Attempt); errors.Is(err, store.ErrNotFound) {
					validate = append(validate, Action{TaskID: task.ID, Kind: ActionValidate,
						Command: "sophon validate " + task.ID})
				} else if err != nil {
					return Report{}, err
				}
			}
			if status.State != store.StateReleased && status.State != store.StateDelivered {
				reviews = append(reviews, reviewActions(task, status.Review)...)
			}
		}
		if showAll || len(entry.Tasks) > 0 {
			report.Missions = append(report.Missions, entry)
		}
	}
	report.Actions = append(report.Actions, verify...)
	report.Actions = append(report.Actions, validate...)
	report.Actions = append(report.Actions, reviews...)
	return report, nil
}

// augmentPullRequest derives the live continuation/reconciliation state from
// typed identity plus a fresh forge and remote observation. Merged PRs become
// terminal; an open exact PR remains an operational delivery surface even
// after its worker copy was released.
func (f *Flow) augmentPullRequest(ctx context.Context, mission store.Mission, status store.TaskStatus) store.TaskStatus {
	if f.deps.DeliveryRemote == nil {
		return status
	}
	task := status.Task
	currentDelivery, deliveryErr := store.ReadDelivery(task.MissionID, task.ID, status.Attempt)
	if deliveryErr == nil && currentDelivery.State == store.DeliveryDeliveredPR {
		delivered := currentDelivery
		pr, head, observeErr := f.observeExactPR(ctx, mission.ProjectPath, delivered)
		if observeErr != nil {
			status.State = store.StateReconciliation
			status.Detail = observeErr.Error()
			return status
		}
		if !strings.EqualFold(head, delivered.HeadSHA) {
			status.State = store.StateReconciliation
			status.Detail = fmt.Sprintf("public head %s differs from delivered revision head %s", head, delivered.HeadSHA)
			return status
		}
		switch pr.State {
		case delivery.PullRequestOpen:
			status.State = store.StateAwaitingFeedback
			status.Detail = "open pull request awaiting feedback: " + delivered.PRURL
		case delivery.PullRequestMerged:
			if _, releaseErr := store.ReadRelease(task.MissionID, task.ID, status.Attempt); releaseErr == nil {
				status.State = store.StateReleased
				status.DeliveryState = string(delivered.State)
				status.Detail = "merged pull request; lease returned"
			} else {
				status.State = store.StateMerged
				status.Detail = "pull request merged: " + delivered.PRURL
			}
		case delivery.PullRequestClosed:
			status.State = store.StateReconciliation
			status.Detail = "pull request closed without merge; operator must choose reopen or replacement"
		}
		return status
	} else if deliveryErr != nil && !errors.Is(deliveryErr, store.ErrNotFound) {
		status.State = store.StateReconciliation
		status.Detail = "cannot read current delivery identity: " + deliveryErr.Error()
		return status
	}

	correction, err := store.ReadCorrection(task.MissionID, task.ID, status.Revision)
	if errors.Is(err, store.ErrNotFound) {
		return status
	}
	if err != nil {
		status.State = store.StateReconciliation
		status.Detail = "cannot read correction identity: " + err.Error()
		return status
	}
	if !store.CorrectionContinuesPullRequest(correction) {
		return status
	}
	expected := store.Delivery{Repository: correction.Repository, Branch: correction.PublicBranch,
		HeadSHA: correction.BaseSHA, BaseRepository: correction.BaseRepository, BaseBranch: correction.BaseBranch,
		PRURL: correction.PRURL, PRNumber: correction.PRNumber, State: store.DeliveryDeliveredPR}
	pr, head, observeErr := f.observeExactPR(ctx, mission.ProjectPath, expected)
	if observeErr == nil && pr.State == delivery.PullRequestOpen && deliveryErr == nil &&
		currentDelivery.State == store.DeliveryPending && strings.EqualFold(currentDelivery.PriorHeadSHA, correction.BaseSHA) &&
		strings.EqualFold(head, currentDelivery.HeadSHA) {
		status.State = store.StateCorrectionAwaitingDelivery
		status.Detail = "correction push observed at exact head; delivery receipt pending recovery"
		return status
	}
	if observeErr != nil || pr.State != delivery.PullRequestOpen || !strings.EqualFold(head, correction.BaseSHA) {
		status.State = store.StateReconciliation
		if observeErr != nil {
			status.Detail = observeErr.Error()
		} else {
			status.Detail = fmt.Sprintf("correction base drifted: state=%s head=%s expected=%s", pr.State, head, correction.BaseSHA)
		}
	}
	return status
}

func reviewActions(task store.Task, review store.ReviewStatus) []Action {
	var actions []Action
	switch review.State {
	case "ready-to-open":
		if review.Posture == "required" {
			actions = append(actions, Action{TaskID: task.ID, Kind: ActionOpenReview,
				Command: "sophon review open " + task.ID})
		}
	case "invalid-evidence", "stale":
		actions = append(actions, Action{TaskID: task.ID, Kind: ActionReviewReconcile,
			Command: "sophon review reconcile " + task.ID + " --json"})
	}
	if review.SessionID != "" && !review.Ended && !review.BridgeRunning &&
		review.State != "invalid-evidence" && review.State != "stale" {
		actions = append(actions, Action{TaskID: task.ID, Kind: ActionReviewReconcile,
			Command: "sophon review reconcile " + task.ID + " --json"})
	}
	for _, sequence := range review.PendingFeedbackSequences {
		actions = append(actions, Action{TaskID: task.ID, Kind: ActionReadReview,
			Command: fmt.Sprintf("sophon review feedback %s --after %d --limit 1 --json", task.ID, sequence-1)})
	}
	for _, sequence := range review.UnroutedChangeSequences {
		actions = append(actions, Action{TaskID: task.ID, Kind: ActionApplyReview,
			Command: fmt.Sprintf("sophon review apply %s --sequence %d --json", task.ID, sequence)})
	}
	if review.ApprovalEligible && !review.ApprovalAcknowledged {
		actions = append(actions, Action{TaskID: task.ID, Kind: ActionReviewApproved,
			Command: fmt.Sprintf("sophon review acknowledge %s --sequence %d --json", task.ID, review.LatestApprovalSequence)})
	}
	return actions
}

// augmentActive observes the current attempt's pane live; adapter failures
// degrade to unknown-pane rather than an error.
func (f *Flow) augmentActive(ctx context.Context, status store.TaskStatus) store.TaskStatus {
	if f.deps.Panes == nil {
		return status
	}
	spawn, err := store.ReadSpawn(status.Task.MissionID, status.Task.ID, status.Attempt)
	if err != nil {
		// A bumped attempt whose spawn never completed has no pane to observe.
		status.Detail = "no spawn receipt for current attempt"
		return status
	}
	state, err := f.deps.Panes.Observe(ctx, spawn.Pane)
	if err != nil {
		status.State = "unknown-pane"
		return status
	}
	status.State = string(state)
	return status
}

// Send submits to the current attempt's exact worker pane with a message and persists
// the (possibly replaced) pane placement back into spawn.json.
func (f *Flow) Send(ctx context.Context, taskID, message string) error {
	return f.sendExact(ctx, taskID, 0, message)
}

// SendExact is the review-correction steering boundary. It refuses if the
// task has moved away from the classified attempt before any Herdr effect.
func (f *Flow) SendExact(ctx context.Context, taskID string, attempt int, message string) error {
	if attempt < 1 {
		return errors.New("send exact requires a positive attempt")
	}
	return f.sendExact(ctx, taskID, attempt, message)
}

func (f *Flow) sendExact(ctx context.Context, taskID string, expectedAttempt int, message string) error {
	if f.deps.Panes == nil {
		return errors.New("flow is not fully configured for send")
	}
	if err := requireNonEmpty(taskID, message); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	release, err := store.Acquire(ctx, "send "+taskID)
	if err != nil {
		return err
	}
	defer release()
	task, err := store.FindTask(taskID)
	if err != nil {
		return err
	}
	attempt, err := currentAttempt(task)
	if err != nil {
		return err
	}
	if expectedAttempt > 0 && attempt != expectedAttempt {
		return fmt.Errorf("send target attempt %d is stale; current attempt is %d", expectedAttempt, attempt)
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return err
	}
	session, err := f.deps.Panes.Submit(ctx, spawn.Pane, message)
	if err != nil {
		return fmt.Errorf("submit worker pane: %w", err)
	}
	spawn.Pane = session
	homeDir, err := datahome.Dir()
	if err != nil {
		return err
	}
	return store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "spawn.json"), spawn)
}
