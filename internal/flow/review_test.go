package flow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/readcode"
	"sophon/internal/store"
)

const (
	liveSession   = "57d91f3ddc544f34e70c1156"
	liveBaseSHA   = "54f2d0d979263780277921e45bf36915e0dcebf6"
	liveHeadSHA   = "a34b90b332419a8a603a82c8a9cffbe0edb71b47"
	secondSession = "67d91f3ddc544f34e70c1157"
	newHeadSHA    = "3333333333333333333333333333333333333333"
)

func prepareRequiredReview(t *testing.T, rig *testRig) (store.Task, store.Spawn) {
	t.Helper()
	ctx := context.Background()
	mission, err := rig.flow.CreateMission(ctx, "/repo", "Review feature", "Implement and review the feature.")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(ctx, mission.ID, "Review the feature", "Implement the complete reviewed behavior.",
		"feature/review-the-feature", "", domain.DeliveryBranch, "go test ./...", domain.ReviewRequired)
	if err != nil {
		t.Fatal(err)
	}
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.LookupEnv("SOPHON_DATA_HOME")
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, rig.git.headSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Validate(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	return task, spawn
}

func configureReviewRevision(rig *testRig, session, head string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rig.review.open = readcode.OpenResult{SchemaVersion: 1, SessionID: session, BaseSHA: rig.git.baseSHA,
		HeadSHA: head, BrowserURL: "http://127.0.0.1:49152/#/review/" + session + "/secret-token", Status: "open"}
	rig.review.status = readcode.StatusResult{SchemaVersion: 1, SessionID: session, Status: "open",
		BaseSHA: rig.git.baseSHA, HeadSHA: head, UpdatedAt: now}
}

func TestLocalCompletionCanBeReviewedButApprovalNeverDelivers(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	mission, err := rig.flow.CreateMission(ctx, "/repo", "Local review", "Implement and review locally")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(ctx, mission.ID, "Local reviewed work", "Implement the reviewed behavior.",
		"", "", domain.DeliveryLocal, "", domain.ReviewRequired)
	if err != nil {
		t.Fatal(err)
	}
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, rig.git.headSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	configureReviewRevision(rig, liveSession, rig.git.headSHA)
	opened, err := rig.flow.ReviewOpen(ctx, task.ID, 1, true)
	if err != nil || opened.HeadSHA != rig.git.headSHA {
		t.Fatalf("local review open = %+v, %v", opened, err)
	}
	approval := readcode.Event{SchemaVersion: 1, SessionID: liveSession, Sequence: 1,
		ID: "99999999-9999-4999-8999-999999999999", CreatedAt: "2026-08-09T12:00:00Z",
		BaseSHA: rig.git.baseSHA, HeadSHA: rig.git.headSHA, Type: "approval", ApprovedHeadSHA: rig.git.headSHA}
	rig.review.polls = []readcode.PollResult{{SchemaVersion: 1, SessionID: liveSession, After: 0, NextCursor: 1, Events: []readcode.Event{approval}}}
	rig.review.status.EventCount, rig.review.status.LastSequence = 1, 1
	if _, err := rig.flow.ReviewReconcile(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.AcknowledgeReviewApproval(ctx, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	if rig.remote.pushes != 0 || rig.remote.creates != 0 {
		t.Fatalf("local approval caused delivery: pushes=%d creates=%d", rig.remote.pushes, rig.remote.creates)
	}
	if _, err := store.ReadDelivery(mission.ID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("local approval created delivery evidence: %v", err)
	}
}

func liveFeedbackAndApproval(base, head string) []readcode.Event {
	created := "2026-08-08T12:00:00Z"
	feedback := readcode.Event{SchemaVersion: 1, SessionID: liveSession, Sequence: 1,
		ID: "11111111-1111-4111-8111-111111111111", CreatedAt: created, BaseSHA: base,
		HeadSHA: head, Type: "feedback", Comments: []readcode.Comment{
			{ID: "22222222-2222-4222-8222-222222222222", Scope: "line", Body: "testing", Path: "README.md", CreatedAt: created,
				Anchor: &readcode.Anchor{Revision: readcode.Revision{BaseSHA: base, HeadSHA: head}, Path: "README.md",
					Side: "new", StartLine: 95, EndLine: 95, ContextHash: "aaaaaaaaaaaaaaaaaaaaaaaa", EndContextHash: "aaaaaaaaaaaaaaaaaaaaaaaa"}},
			{ID: "33333333-3333-4333-8333-333333333333", Scope: "general", Body: "Please make the explanation clearer.", CreatedAt: created},
		}}
	approval := readcode.Event{SchemaVersion: 1, SessionID: liveSession, Sequence: 2,
		ID: "44444444-4444-4444-8444-444444444444", CreatedAt: "2026-08-08T12:01:00Z",
		BaseSHA: base, HeadSHA: head, Type: "approval", ApprovedHeadSHA: head}
	return []readcode.Event{feedback, approval}
}

func TestRequiredReviewLiveFixtureCorrectionAndExactHeadApproval(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	rig.git.baseSHA, rig.git.headSHA, rig.remote.headSHA = liveBaseSHA, liveHeadSHA, liveHeadSHA
	ctx := context.Background()
	task, _ := prepareRequiredReview(t, rig)

	report, err := rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Actions) != 1 || report.Actions[0].Kind != ActionOpenReview {
		t.Fatalf("actions before open = %+v", report.Actions)
	}
	configureReviewRevision(rig, liveSession, liveHeadSHA)
	opened, err := rig.flow.ReviewOpen(ctx, task.ID, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if opened.SessionID != liveSession || opened.BaseSHA != liveBaseSHA || opened.HeadSHA != liveHeadSHA {
		t.Fatalf("opened = %+v", opened)
	}
	bindingBytes, err := os.ReadFile(store.ReviewBindingPath(home, task.MissionID, task.ID, 1))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bindingBytes), "secret-token") || strings.Contains(string(bindingBytes), "127.0.0.1") ||
		strings.Contains(string(bindingBytes), "/pool/worktree") {
		t.Fatalf("canonical binding leaked capability or path: %s", bindingBytes)
	}

	events := liveFeedbackAndApproval(liveBaseSHA, liveHeadSHA)
	rig.review.polls = []readcode.PollResult{{SchemaVersion: 1, SessionID: liveSession, After: 0,
		NextCursor: 2, Events: events}}
	rig.review.status.EventCount, rig.review.status.LastSequence = 2, 2
	reconciled, err := rig.flow.ReviewReconcile(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Cursor != 2 || len(reconciled.Ingested) != 2 {
		t.Fatalf("reconcile = %+v", reconciled)
	}
	feedback, err := rig.flow.ReviewFeedback(task.ID, 0, 1)
	if err != nil || len(feedback.Events) != 1 || len(feedback.Events[0].Comments) != 2 ||
		feedback.Events[0].Comments[0].Path != "README.md" || feedback.Events[0].Comments[0].Anchor.StartLine != 95 {
		t.Fatalf("feedback = %+v, %v", feedback, err)
	}
	status := DeriveReview(mustReloadTask(t, task))
	if status.ApprovalEligible || status.State != "feedback" || status.LatestApprovalSequence != 2 {
		t.Fatalf("unprocessed feedback incorrectly approved = %+v", status)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("delivery before feedback classification = %v", err)
	}
	decision, err := rig.flow.ClassifyReviewFeedback(ctx, task.ID, 1, store.ReviewDispositionRequestedChanges)
	if err != nil || decision.Sequence != 1 {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
	report, err = rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Actions) != 2 || report.Actions[0].Kind != ActionReviewReconcile || report.Actions[1].Kind != ActionApplyReview {
		t.Fatalf("requested-change actions = %+v", report.Actions)
	}
	route, err := rig.flow.ApplyReviewFeedback(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if route.TargetRevision != 2 || route.TargetAttempt != 2 || route.Method != store.ReviewRouteRevision {
		t.Fatalf("review route = %+v", route)
	}
	correction, err := store.ReadCorrection(task.MissionID, task.ID, 2)
	if err != nil || store.CorrectionSource(correction) != store.CorrectionSourceReadCode ||
		correction.ReviewAttempt != 1 || correction.ReviewSession != liveSession ||
		len(correction.ReviewFeedback) != 1 || correction.ReviewFeedback[0] != 1 {
		t.Fatalf("review correction = %+v, %v", correction, err)
	}
	if strings.Contains(correction.Objective, "testing") || strings.Contains(correction.Objective, "make the explanation") ||
		!strings.Contains(correction.Objective, "sequence") {
		t.Fatalf("correction intent leaked arbitrary comment bodies or lost sequence: %q", correction.Objective)
	}
	if review := DeriveReview(mustReloadTask(t, task)); review.ApprovalEligible {
		t.Fatalf("old approval survived requested changes: %+v", review)
	}

	// A correction is a new attempt/revision created by the landed revision
	// owner. Old bindings, comments,
	// and approval stay as history and cannot approve the new exact head.
	rig.git.headSHA = newHeadSHA
	spawn2, err := store.ReadSpawn(task.MissionID, task.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	rig.git.snapshot = gitSnapshot(newHeadSHA, spawn2.Branch, true)
	resultPath := writeResult(t, home, spawn2, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 2, newHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn2)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Validate(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	current := mustReloadTask(t, task)
	if stale := DeriveReview(current); stale.State != "ready-to-open" || stale.ApprovalEligible {
		t.Fatalf("old approval attached to corrected head: %+v", stale)
	}
	if old, err := store.ReadReviewEvents(task.MissionID, task.ID, 1); err != nil || len(old) != 2 {
		t.Fatalf("old review history = %+v, %v", old, err)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("new revision delivered before approval: %v", err)
	}

	configureReviewRevision(rig, secondSession, newHeadSHA)
	if _, err := rig.flow.ReviewOpen(ctx, task.ID, 2, true); err != nil {
		t.Fatal(err)
	}
	approval := readcode.Event{SchemaVersion: 1, SessionID: secondSession, Sequence: 1,
		ID: "55555555-5555-4555-8555-555555555555", CreatedAt: "2026-08-08T12:02:00Z",
		BaseSHA: liveHeadSHA, HeadSHA: newHeadSHA, Type: "approval", ApprovedHeadSHA: newHeadSHA}
	rig.review.polls = []readcode.PollResult{{SchemaVersion: 1, SessionID: secondSession, After: 0,
		NextCursor: 1, Events: []readcode.Event{approval}}}
	rig.review.status.EventCount, rig.review.status.LastSequence = 1, 1
	if _, err := rig.flow.ReviewReconcile(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	status = DeriveReview(mustReloadTask(t, task))
	if !status.ApprovalEligible || status.HeadSHA != newHeadSHA {
		t.Fatalf("new exact approval not eligible = %+v", status)
	}
	if rig.remote.pushes != 0 {
		t.Fatal("review approval caused an automatic delivery")
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("approval substituted for explicit confirmation: %v", err)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
	if rig.remote.pushes != 1 {
		t.Fatalf("confirmed exact-head delivery pushes = %d", rig.remote.pushes)
	}
	if len(rig.delGit.commitBases) == 0 || rig.delGit.commitBases[len(rig.delGit.commitBases)-1] != liveBaseSHA {
		t.Fatalf("first delivery after review correction preflighted bases %v, want original %s", rig.delGit.commitBases, liveBaseSHA)
	}
}

func TestReviewIngestReplayConflictGapAndImmediateDeliveryDrift(t *testing.T) {
	useHome(t)
	rig := newRig()
	ctx := context.Background()
	task, _ := prepareRequiredReview(t, rig)
	configureReviewRevision(rig, liveSession, testHeadSHA)
	if _, err := rig.flow.ReviewOpen(ctx, task.ID, 1, true); err != nil {
		t.Fatal(err)
	}
	binding, err := store.ReadReviewBinding(task.MissionID, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	events := liveFeedbackAndApproval(testBaseSHA, testHeadSHA)
	duplicateID := append([]readcode.Event(nil), events...)
	duplicateID[1].ID = duplicateID[0].ID
	if _, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, duplicateID); !errors.Is(err, ErrReviewReconcile) {
		t.Fatalf("duplicate product event id = %v", err)
	}
	if cursor, err := store.ReviewCursor(task.MissionID, task.ID, 1); err != nil || cursor != 0 {
		t.Fatalf("invalid batch advanced canonical cursor to %d: %v", cursor, err)
	}
	if got, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, events); err != nil || len(got) != 2 {
		t.Fatalf("initial ingest = %v, %v", got, err)
	}
	if got, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, events); err != nil || len(got) != 0 {
		t.Fatalf("idempotent replay = %v, %v", got, err)
	}
	conflict := events[0]
	conflict.ID = "99999999-9999-4999-8999-999999999999"
	if _, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, []readcode.Event{conflict}); !errors.Is(err, ErrReviewReconcile) {
		t.Fatalf("conflicting replay = %v", err)
	}
	mismatchedHead := events[0]
	mismatchedHead.HeadSHA = testBaseSHA
	if _, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, []readcode.Event{mismatchedHead}); err == nil {
		t.Fatal("mismatched review head was accepted")
	}
	gap := events[1]
	gap.Sequence = 4
	gap.ID = "88888888-8888-4888-8888-888888888888"
	if _, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, []readcode.Event{gap}); !errors.Is(err, ErrReviewReconcile) {
		t.Fatalf("cursor gap = %v", err)
	}
	end := readcode.Event{SchemaVersion: 1, SessionID: liveSession, Sequence: 3,
		ID: "77777777-7777-4777-8777-777777777777", CreatedAt: "2026-08-08T12:03:00Z",
		BaseSHA: testBaseSHA, HeadSHA: testHeadSHA, Type: "end"}
	afterEnd := readcode.Event{SchemaVersion: 1, SessionID: liveSession, Sequence: 4,
		ID: "66666666-6666-4666-8666-666666666666", CreatedAt: "2026-08-08T12:04:00Z",
		BaseSHA: testBaseSHA, HeadSHA: testHeadSHA, Type: "approval", ApprovedHeadSHA: testHeadSHA}
	if _, _, err := rig.flow.IngestReviewEvents(ctx, task.ID, 1, binding, []readcode.Event{end, afterEnd}); !errors.Is(err, ErrReviewReconcile) {
		t.Fatalf("event after terminal end = %v", err)
	}
	if cursor, err := store.ReviewCursor(task.MissionID, task.ID, 1); err != nil || cursor != 2 {
		t.Fatalf("terminally invalid batch advanced canonical cursor to %d: %v", cursor, err)
	}
	if _, err := rig.flow.ClassifyReviewFeedback(ctx, task.ID, 1, store.ReviewDispositionNonActionable); err != nil {
		t.Fatal(err)
	}
	rig.review.status.EventCount, rig.review.status.LastSequence = 3, 3
	if _, err := rig.flow.Deliver(ctx, task.ID, true); !errors.Is(err, ErrReviewReconcile) {
		t.Fatalf("unreconciled later product event delivery = %v", err)
	}
	if rig.remote.pushes != 0 {
		t.Fatal("cursor drift reached external delivery effect")
	}
}

func TestReviewCorrectionUsesSameRevisionOwnerAfterWorkerRelease(t *testing.T) {
	useHome(t)
	rig := newRig()
	ctx := context.Background()
	task, _ := prepareRequiredReview(t, rig)
	configureReviewRevision(rig, liveSession, testHeadSHA)
	if _, err := rig.flow.ReviewOpen(ctx, task.ID, 1, true); err != nil {
		t.Fatal(err)
	}
	feedback := liveFeedbackAndApproval(testBaseSHA, testHeadSHA)[:1]
	rig.review.polls = []readcode.PollResult{{SchemaVersion: 1, SessionID: liveSession, After: 0,
		NextCursor: 1, Events: feedback}}
	rig.review.status.EventCount, rig.review.status.LastSequence = 1, 1
	if _, err := rig.flow.ReviewReconcile(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.ClassifyReviewFeedback(ctx, task.ID, 1, store.ReviewDispositionRequestedChanges); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.ReleaseLeaseAttempt(ctx, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	route, err := rig.flow.ApplyReviewFeedback(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	spawn, err := store.ReadSpawn(task.MissionID, task.ID, route.TargetAttempt)
	if err != nil || spawn.Revision != 2 || spawn.BaseSHA != testHeadSHA {
		t.Fatalf("released-worker correction spawn = %+v, %v", spawn, err)
	}
	correction, err := store.ReadCorrection(task.MissionID, task.ID, 2)
	if err != nil || store.CorrectionSource(correction) != store.CorrectionSourceReadCode ||
		store.CorrectionContinuesPullRequest(correction) {
		t.Fatalf("released-worker correction = %+v, %v", correction, err)
	}
}

func TestReviewCorrectionOnDeliveredPRRetainsExactContinuationIdentity(t *testing.T) {
	rig, _, task, spawn, delivered := prepareOpenPRForRevision(t)
	ctx := context.Background()
	if _, err := rig.flow.SetReviewPosture(ctx, task.ID, domain.ReviewOptional); err != nil {
		t.Fatal(err)
	}
	configureReviewRevision(rig, liveSession, testHeadSHA)
	if _, err := rig.flow.ReviewOpen(ctx, task.ID, spawn.Attempt, true); err != nil {
		t.Fatal(err)
	}
	feedback := liveFeedbackAndApproval(testBaseSHA, testHeadSHA)[:1]
	rig.review.polls = []readcode.PollResult{{SchemaVersion: 1, SessionID: liveSession, After: 0,
		NextCursor: 1, Events: feedback}}
	rig.review.status.EventCount, rig.review.status.LastSequence = 1, 1
	if _, err := rig.flow.ReviewReconcile(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.ClassifyReviewFeedback(ctx, task.ID, 1, store.ReviewDispositionRequestedChanges); err != nil {
		t.Fatal(err)
	}
	route, err := rig.flow.ApplyReviewFeedback(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	correction, err := store.ReadCorrection(task.MissionID, task.ID, route.TargetRevision)
	if err != nil || store.CorrectionSource(correction) != store.CorrectionSourceReadCode ||
		!store.CorrectionContinuesPullRequest(correction) || correction.PRURL != delivered.PRURL ||
		correction.PRNumber != delivered.PRNumber || correction.PublicBranch != delivered.Branch ||
		correction.BaseSHA != delivered.HeadSHA {
		t.Fatalf("delivered-PR review correction = %+v, %v", correction, err)
	}
	correctionSpawn, err := store.ReadSpawn(task.MissionID, task.ID, route.TargetAttempt)
	if err != nil || correctionSpawn.Revision != 2 || correctionSpawn.BaseSHA != delivered.HeadSHA {
		t.Fatalf("delivered-PR review spawn = %+v, %v", correctionSpawn, err)
	}
}

func TestReviewPostureCompatibilityAndEscalationHistory(t *testing.T) {
	useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	// Existing task JSON without a review field remains off.
	task.ReviewPosture = ""
	if posture, err := store.EffectiveReviewPosture(task); err != nil || posture != domain.ReviewOff {
		t.Fatalf("legacy posture = %s, %v", posture, err)
	}
	if _, err := rig.flow.ReviewOpen(ctx, task.ID, 0, true); !errors.Is(err, ErrReviewOff) {
		t.Fatalf("off task review open = %v", err)
	}
	first, err := rig.flow.SetReviewPosture(ctx, task.ID, domain.ReviewOptional)
	if err != nil || first.From != domain.ReviewOff || first.To != domain.ReviewOptional {
		t.Fatalf("first transition = %+v, %v", first, err)
	}
	second, err := rig.flow.SetReviewPosture(ctx, task.ID, domain.ReviewRequired)
	if err != nil || second.Sequence != 2 || second.From != domain.ReviewOptional {
		t.Fatalf("second transition = %+v, %v", second, err)
	}
	if _, err := rig.flow.SetReviewPosture(ctx, task.ID, domain.ReviewOptional); err == nil {
		t.Fatal("review posture downgrade was accepted")
	}
}

func mustReloadTask(t *testing.T, task store.Task) store.Task {
	t.Helper()
	loaded, err := store.ReadTask(task.MissionID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func gitSnapshot(head, branch string, clean bool) gitcontrol.Snapshot {
	return gitcontrol.Snapshot{Head: head, Branch: branch, Clean: clean}
}
