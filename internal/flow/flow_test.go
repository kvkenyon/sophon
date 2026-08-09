package flow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/delivery"
	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/herdr"
	"sophon/internal/store"
	"sophon/internal/treehouse"
	"sophon/internal/validation"
)

const (
	testBaseSHA = "1111111111111111111111111111111111111111"
	testHeadSHA = "2222222222222222222222222222222222222222"
	testRepo    = "github.com/acme/repo"
)

func useHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(datahome.OverrideEnv, home)
	return home
}

type testRig struct {
	flow     *Flow
	git      *fakeGit
	leases   *fakeLeases
	panes    *fakePanes
	delGit   *fakeDeliveryGit
	remote   *fakeDeliveryRemote
	validate *fakeValidator
	review   *fakeReviewProduct
}

func newRig() *testRig {
	rig := &testRig{
		git: &fakeGit{baseSHA: testBaseSHA, headSHA: testHeadSHA},
		leases: &fakeLeases{alloc: treehouse.Allocation{
			WorktreePath: "/pool/worktree-1", LeaseID: "lease-1"}},
		panes:    &fakePanes{session: herdr.Session{WorkspaceID: "ws-1", TabID: "tab-1"}},
		delGit:   &fakeDeliveryGit{repository: testRepo},
		remote:   &fakeDeliveryRemote{headSHA: testHeadSHA},
		validate: &fakeValidator{result: validation.Result{Status: validation.Passed, ExitCode: 0}},
		review:   &fakeReviewProduct{},
	}
	rig.flow = New(Deps{
		Git: rig.git, Leases: rig.leases, Panes: rig.panes,
		DeliveryGit: rig.delGit, DeliveryRemote: rig.remote,
		ReviewProduct: rig.review,
		NewValidator:  func(string) Validator { return rig.validate },
	})
	return rig
}

// leaseStatus makes the fake Treehouse report the spawn's exact lease as live.
func (r *testRig) leaseStatus(spawn store.Spawn) {
	r.leases.mu.Lock()
	defer r.leases.mu.Unlock()
	r.leases.statuses = []treehouse.WorktreeStatus{{
		WorktreePath: spawn.WorktreePath, Status: "leased",
		LeaseID: spawn.LeaseID, LeaseHolder: spawn.LeaseHolder,
	}}
}

func (r *testRig) createMissionAndTask(t *testing.T, mode domain.DeliveryMode, validationCommand string) (store.Mission, store.Task) {
	t.Helper()
	ctx := context.Background()
	mission, err := r.flow.CreateMission(ctx, "/repo", "Ship feature", "Implement and deliver the feature.")
	if err != nil {
		t.Fatal(err)
	}
	task, err := r.flow.CreateTask(ctx, mission.ID, "Add the feature", "Implement the complete product behavior.",
		"feature/add-the-feature", "", mode, validationCommand)
	if err != nil {
		t.Fatal(err)
	}
	return mission, task
}

func (r *testRig) prepareVerified(t *testing.T, mode domain.DeliveryMode) (store.Task, store.Spawn) {
	t.Helper()
	home, err := datahome.Dir()
	if err != nil {
		t.Fatal(err)
	}
	_, task := r.createMissionAndTask(t, mode, "")
	spawn, err := r.flow.Spawn(context.Background(), task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := r.flow.PublishResult(context.Background(), task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	r.leaseStatus(spawn)
	if _, err := r.flow.VerifyComplete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	return task, spawn
}

const validResult = `{
  "version": 1,
  "status": "completed",
  "summary": "implemented the feature",
  "verification": [{"command": "go test ./...", "exit_code": 0}],
  "changed_files": ["feature.go"],
  "risks": []
}`

// writeResult drops a worker completion into the exact generated staging path;
// publication writes canonical result.json only after validation.
func writeResult(t *testing.T, home string, spawn store.Spawn, content string) string {
	t.Helper()
	dir := store.AttemptDir(home, spawn.MissionID, spawn.TaskID, spawn.Attempt)
	path := filepath.Join(dir, store.CompletionSubmissionName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeReport(t *testing.T, home string, spawn store.Spawn, status, reason string, dirty bool) string {
	t.Helper()
	path := store.AttemptPath(home, spawn.MissionID, spawn.TaskID, spawn.Attempt, store.ReportSubmissionName)
	report := store.WorkerReport{Version: 1, Status: status, TaskID: spawn.TaskID, Attempt: spawn.Attempt,
		HeadSHA: testHeadSHA, Reason: reason, Verification: []domain.VerificationResult{},
		Evidence: []string{"task brief targets a different subsystem"}, ChangedFiles: []string{"preserved.go"},
		DirtyWork: dirty, Risks: []string{"operator decision required"}}
	if err := store.Publish(path, report); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHappyPathBranchDelivery(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	mission, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "go test ./...")

	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if spawn.Attempt != 1 || spawn.BaseSHA != testBaseSHA || spawn.LeaseHolder != LeaseHolder(task.ID, 1) {
		t.Fatalf("spawn = %+v", spawn)
	}
	wantBranch := TaskBranch(task.Title, task.ID, 1)
	if spawn.Branch != wantBranch {
		t.Fatalf("branch = %q, want %q", spawn.Branch, wantBranch)
	}
	// The brief is published into the attempt dir with the completion contract.
	brief, err := os.ReadFile(store.AttemptPath(home, mission.ID, task.ID, 1, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Generated task brief", "sophon worker complete " + task.ID + " --attempt 1",
		"sophon worker report " + task.ID + " --attempt 1", store.CompletionSubmissionName, store.ReportSubmissionName,
		"`version`, `status`, `summary`, `verification`, `changed_files`, and `risks`",
		"Implement the complete product behavior.", "Public delivery title: Add the feature",
		"Public delivery branch: `feature/add-the-feature`", "public-quality subject"} {
		if !strings.Contains(string(brief), want) {
			t.Fatalf("brief missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(store.WorkerSkillDir(home, task.ID, 1), "coding-guidelines", "SKILL.md")); err != nil {
		t.Fatalf("worker skills not materialized: %v", err)
	}
	// A second plain spawn is refused.
	if _, err := rig.flow.Spawn(ctx, task.ID, false); !errors.Is(err, ErrAttemptsExist) {
		t.Fatalf("err = %v, want ErrAttemptsExist", err)
	}

	resultPath := writeResult(t, home, spawn, validResult)
	digest, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest = %q", digest)
	}
	// A head claim that does not match the worktree is refused.
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testBaseSHA, resultPath); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("err = %v, want ErrHeadMismatch", err)
	}
	// Derived state is ready pending verification.
	status, err := store.Derive(mustTask(t, task.ID))
	if err != nil || status.State != store.StateReady {
		t.Fatalf("status = %+v, %v", status, err)
	}

	rig.leaseStatus(spawn)
	outcome, err := rig.flow.VerifyComplete(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.HeadSHA != testHeadSHA || outcome.ResultSHA256 != digest {
		t.Fatalf("outcome = %+v", outcome)
	}

	validationRecord, err := rig.flow.Validate(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !validationRecord.Passed || validationRecord.HeadSHA != testHeadSHA {
		t.Fatalf("validation = %+v", validationRecord)
	}

	if _, err := rig.flow.Deliver(ctx, task.ID, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("err = %v, want ErrNotConfirmed", err)
	}
	receipt, err := rig.flow.Deliver(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != store.DeliveryDeliveredBranch || receipt.Repository != testRepo ||
		receipt.HeadSHA != testHeadSHA || receipt.DeliveredAt == nil {
		t.Fatalf("receipt = %+v", receipt)
	}
	// Idempotent re-run returns the same receipt without new externals.
	again, err := rig.flow.Deliver(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != receipt.State || again.HeadSHA != receipt.HeadSHA ||
		!again.DeliveredAt.Equal(*receipt.DeliveredAt) || rig.remote.pushes != 1 {
		t.Fatalf("again = %+v, pushes = %d", again, rig.remote.pushes)
	}
	if status, _ := store.Derive(mustTask(t, task.ID)); status.State != store.StateDelivered {
		t.Fatalf("status = %+v", status)
	}

	releaseRecord, err := rig.flow.ReleaseLease(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if releaseRecord.LeaseID != spawn.LeaseID || releaseRecord.LeaseHolder != spawn.LeaseHolder {
		t.Fatalf("release = %+v", releaseRecord)
	}
	// Re-running release returns the existing receipt without a second return.
	calls := len(rig.leases.releases)
	if _, err := rig.flow.ReleaseLease(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if len(rig.leases.releases) != calls {
		t.Fatal("release was not idempotent")
	}
}

func TestHappyPathPRDelivery(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryPR, "")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	// No validation command configured: validate is a descriptive error, not a receipt.
	if _, err := rig.flow.Validate(ctx, task.ID); err == nil || !strings.Contains(err.Error(), "no validation command") {
		t.Fatalf("err = %v", err)
	}
	rig.remote.pr = nil
	rig.remote.create = delivery.PullRequest{Repository: testRepo, Branch: task.DeliveryBranch,
		HeadSHA: testHeadSHA, URL: "https://github.com/acme/repo/pull/7", Number: 7}
	receipt, err := rig.flow.Deliver(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != store.DeliveryDeliveredPR || receipt.PRNumber != 7 ||
		receipt.PRURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if rig.remote.input.Branch != task.DeliveryBranch || rig.remote.input.Title != task.Title ||
		!strings.Contains(rig.remote.input.Body, "## Summary") || strings.Contains(strings.ToLower(rig.remote.input.Body), "sophon") {
		t.Fatalf("public pull request input = %+v", rig.remote.input)
	}
}

func TestPR6818HistoricalShapeContinuesAsSamePR(t *testing.T) {
	home := useHome(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pr-6818-open-correction.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Repository             string `json:"repository"`
		PublicBranch           string `json:"public_branch"`
		BaseRepository         string `json:"base_repository"`
		BaseBranch             string `json:"base_branch"`
		PRURL                  string `json:"pr_url"`
		PRNumber               int    `json:"pr_number"`
		OpenPRHead             string `json:"open_pr_head"`
		VerifiedCorrectionHead string `json:"verified_correction_head"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	rig := newRig()
	rig.delGit.repository = fixture.Repository
	mission, task := rig.createMissionAndTask(t, domain.DeliveryPR, "")
	spawn, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the exact historical public identity that predates today's intake
	// sanitizer. New tasks cannot create this branch; correction can only retain
	// and normally fast-forward the already-public ref.
	task, err = store.ReadTask(mission.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Title = "HOME-111: Migrate Tesla Fleet API client to BaseClient"
	task.DeliveryBranch = fixture.PublicBranch
	// PR 6818 predates revision pointers and canonical base identity in delivery
	// receipts. Keep those fields absent so this fixture proves real migration
	// compatibility instead of silently upgrading the old evidence in place.
	task.CurrentRevision = 0
	if err := store.Publish(store.TaskPath(home, mission.ID, task.ID), task); err != nil {
		t.Fatal(err)
	}
	legacySpawn := spawn
	legacySpawn.Revision = 0
	if err := store.Publish(store.AttemptPath(home, mission.ID, task.ID, 1, "spawn.json"), legacySpawn); err != nil {
		t.Fatal(err)
	}
	firstDelivery := store.Delivery{TaskID: task.ID, Attempt: 1, Mode: domain.DeliveryPR,
		Repository: fixture.Repository, Branch: fixture.PublicBranch, HeadSHA: fixture.OpenPRHead,
		State: store.DeliveryDeliveredPR, PRURL: fixture.PRURL, PRNumber: fixture.PRNumber,
		IntentAt: time.Now().UTC()}
	deliveredAt := time.Now().UTC()
	firstDelivery.DeliveredAt = &deliveredAt
	if err := store.Publish(store.AttemptPath(home, mission.ID, task.ID, 1, "delivery.json"), firstDelivery); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(store.AttemptPath(home, mission.ID, task.ID, 1, "release.json"), store.Release{
		TaskID: task.ID, Attempt: 1, LeaseID: spawn.LeaseID,
		LeaseHolder: spawn.LeaseHolder, ReleasedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rig.remote.headSHA = fixture.OpenPRHead
	rig.remote.branchExists = true
	rig.remote.branch = fixture.PublicBranch
	rig.remote.observed = &delivery.PullRequest{Repository: fixture.Repository, Branch: fixture.PublicBranch,
		HeadSHA: fixture.OpenPRHead, BaseRepository: fixture.BaseRepository, BaseBranch: fixture.BaseBranch,
		State: delivery.PullRequestOpen, URL: fixture.PRURL, Number: fixture.PRNumber}
	legacyDeliveryPath := store.AttemptPath(home, mission.ID, task.ID, 1, "delivery.json")
	legacyDeliveryBytes, err := os.ReadFile(legacyDeliveryPath)
	if err != nil {
		t.Fatal(err)
	}
	status, err := rig.flow.Status(context.Background())
	if err != nil || len(status.Missions) != 1 || len(status.Missions[0].Tasks) != 1 ||
		status.Missions[0].Tasks[0].State != store.StateAwaitingFeedback {
		t.Fatalf("legacy PR 6818 operational status = %+v, %v", status, err)
	}

	correctedSpawn, err := rig.flow.Revise(context.Background(), task.ID, "Accepted review correction",
		"Apply only the requested BaseClient correction beyond the current PR head.", false)
	if err != nil {
		t.Fatal(err)
	}
	if correctedSpawn.Revision != 2 || correctedSpawn.Attempt != 2 || correctedSpawn.BaseSHA != fixture.OpenPRHead {
		t.Fatalf("PR 6818 correction spawn = %+v", correctedSpawn)
	}
	correction, err := store.ReadCorrection(mission.ID, task.ID, 2)
	if err != nil || correction.BaseRepository != fixture.BaseRepository || correction.BaseBranch != fixture.BaseBranch {
		t.Fatalf("PR 6818 canonical correction identity = %+v, %v", correction, err)
	}
	resultPath := writeResult(t, home, correctedSpawn, validResult)
	rig.git.headSHA = fixture.VerifiedCorrectionHead
	if _, err := rig.flow.PublishResult(context.Background(), task.ID, 2, fixture.VerifiedCorrectionHead, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(correctedSpawn)
	outcome, err := rig.flow.VerifyComplete(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.HeadSHA != fixture.VerifiedCorrectionHead {
		t.Fatalf("PR 6818 correction outcome = %+v", outcome)
	}
	receipt, err := rig.flow.Deliver(context.Background(), task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PRNumber != fixture.PRNumber || receipt.PRURL != fixture.PRURL ||
		receipt.Branch != fixture.PublicBranch || receipt.PriorHeadSHA != fixture.OpenPRHead ||
		receipt.HeadSHA != fixture.VerifiedCorrectionHead || rig.remote.creates != 0 || rig.remote.fastForwards != 1 {
		t.Fatalf("PR 6818 same-PR receipt = %+v, creates=%d ff=%d", receipt, rig.remote.creates, rig.remote.fastForwards)
	}
	unchangedDeliveryBytes, err := os.ReadFile(legacyDeliveryPath)
	if err != nil || string(unchangedDeliveryBytes) != string(legacyDeliveryBytes) {
		t.Fatalf("legacy PR 6818 delivery evidence changed: %v", err)
	}
}

func prepareOpenPRForRevision(t *testing.T) (*testRig, store.Mission, store.Task, store.Spawn, store.Delivery) {
	t.Helper()
	useHome(t)
	rig := newRig()
	mission, task := rig.createMissionAndTask(t, domain.DeliveryPR, "")
	spawn, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := datahome.Dir()
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(context.Background(), task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	rig.remote.create = delivery.PullRequest{URL: "https://github.com/acme/repo/pull/17", Number: 17}
	receipt, err := rig.flow.Deliver(context.Background(), task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	rig.panes.observe = herdr.StateLost
	return rig, mission, task, spawn, receipt
}

func TestOpenPullRequestStatusDerivesTerminalAndReconciliationStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testRig)
		want   string
	}{
		{"merged terminal", func(r *testRig) { r.remote.observed.State = delivery.PullRequestMerged }, store.StateMerged},
		{"closed unmerged", func(r *testRig) { r.remote.observed.State = delivery.PullRequestClosed }, store.StateReconciliation},
		{"public head drift", func(r *testRig) {
			r.remote.headSHA = "3333333333333333333333333333333333333333"
			r.remote.observed.HeadSHA = r.remote.headSHA
		}, store.StateReconciliation},
		{"base drift", func(r *testRig) { r.remote.observed.BaseBranch = "release" }, store.StateReconciliation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig, _, _, _, _ := prepareOpenPRForRevision(t)
			tt.mutate(rig)
			report, err := rig.flow.Status(context.Background())
			if err != nil || len(report.Missions) != 1 || len(report.Missions[0].Tasks) != 1 ||
				report.Missions[0].Tasks[0].State != tt.want {
				t.Fatalf("status = %+v, %v; want %s", report, err, tt.want)
			}
		})
	}
}

func TestReviseRefusesExceptionalOpenPRPathsWithoutLosingEvidence(t *testing.T) {
	newHead := "3333333333333333333333333333333333333333"
	tests := []struct {
		name     string
		mutate   func(*testRig, store.Delivery)
		accept   bool
		contains string
	}{
		{"merged", func(r *testRig, _ store.Delivery) { r.remote.observed.State = delivery.PullRequestMerged }, false, "merged pull request is terminal"},
		{"closed", func(r *testRig, _ store.Delivery) { r.remote.observed.State = delivery.PullRequestClosed }, false, "closed-unmerged"},
		{"deleted branch", func(r *testRig, _ store.Delivery) { r.remote.branchExists = false }, false, "branch was deleted"},
		{"unaccepted drift", func(r *testRig, _ store.Delivery) { r.remote.headSHA = newHead; r.remote.observed.HeadSHA = newHead }, false, "accept-external-head"},
		{"wrong repository", func(r *testRig, _ store.Delivery) { r.remote.observed.Repository = "github.com/other/repo" }, false, "identity changed"},
		{"wrong base", func(r *testRig, _ store.Delivery) { r.remote.observed.BaseBranch = "release" }, false, "identity changed"},
		{"wrong branch", func(r *testRig, _ store.Delivery) { r.remote.observed.Branch = "other/branch" }, false, "identity changed"},
		{"non descendant reconciliation", func(r *testRig, _ store.Delivery) {
			r.remote.headSHA = newHead
			r.remote.observed.HeadSHA = newHead
			r.delGit.descendantErr = errors.New("not descendant")
		}, true, "non-fast-forward public history"},
		{"dirty prior copy", func(r *testRig, _ store.Delivery) { r.delGit.verifyErr = errors.New("dirty") }, false, "dirty or unresolved"},
		{"live prior worker", func(r *testRig, _ store.Delivery) { r.panes.observe = herdr.StateRunning }, false, "live delivered worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig, _, task, _, delivered := prepareOpenPRForRevision(t)
			before, err := store.ReadDelivery(task.MissionID, task.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(rig, delivered)
			if _, err := rig.flow.Revise(context.Background(), task.ID, "accepted feedback", "bounded correction", tt.accept); err == nil ||
				!strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("revise error = %v, want %q", err, tt.contains)
			}
			after, err := store.ReadDelivery(task.MissionID, task.ID, 1)
			if err != nil || after.HeadSHA != before.HeadSHA || after.PRNumber != before.PRNumber || after.State != before.State {
				t.Fatalf("prior evidence changed: before=%+v after=%+v err=%v", before, after, err)
			}
			current, err := store.ReadTask(task.MissionID, task.ID)
			if err != nil || current.CurrentAttempt != 1 || current.CurrentRevision != 1 {
				t.Fatalf("refusal advanced task identity: %+v, %v", current, err)
			}
		})
	}
}

func TestReviseRefusesDuplicateWhileCorrectionIsUnlanded(t *testing.T) {
	rig, _, task, _, _ := prepareOpenPRForRevision(t)
	spawn, err := rig.flow.Revise(context.Background(), task.ID, "accepted feedback", "bounded correction", false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadCorrection(task.MissionID, task.ID, spawn.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Revise(context.Background(), task.ID, "duplicate", "different correction", false); err == nil ||
		!strings.Contains(err.Error(), "unlanded revision") {
		t.Fatalf("duplicate correction error = %v", err)
	}
	after, err := store.ReadCorrection(task.MissionID, task.ID, spawn.Revision)
	if err != nil || after.Reason != before.Reason || after.Objective != before.Objective || !after.AcceptedAt.Equal(before.AcceptedAt) {
		t.Fatalf("immutable correction changed: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestReviseRecoversExactIntentPublishedBeforeTaskPointer(t *testing.T) {
	rig, _, task, _, delivered := prepareOpenPRForRevision(t)
	acceptedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	correction := store.Correction{
		Version: 1, TaskID: task.ID, MissionID: task.MissionID, Revision: 2,
		PriorRevision: 1, PriorAttempt: 1, Reason: "accepted feedback", Objective: "bounded correction",
		Repository: delivered.Repository, PublicBranch: delivered.Branch, PRURL: delivered.PRURL,
		PRNumber: delivered.PRNumber, BaseRepository: delivered.BaseRepository,
		BaseBranch: delivered.BaseBranch, BaseSHA: delivered.HeadSHA, AcceptedAt: acceptedAt,
	}
	if err := store.CreateCorrection(correction); err != nil {
		t.Fatal(err)
	}
	spawn, err := rig.flow.Revise(context.Background(), task.ID, correction.Reason, correction.Objective, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.ReadCorrection(task.MissionID, task.ID, 2)
	if err != nil || spawn.Revision != 2 || spawn.Attempt != 2 || !loaded.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("recovered intent spawn=%+v correction=%+v err=%v", spawn, loaded, err)
	}
}

func TestReviseRefusesDifferentIntentPublishedBeforeTaskPointer(t *testing.T) {
	rig, _, task, _, delivered := prepareOpenPRForRevision(t)
	correction := store.Correction{
		Version: 1, TaskID: task.ID, MissionID: task.MissionID, Revision: 2,
		PriorRevision: 1, PriorAttempt: 1, Reason: "accepted feedback", Objective: "first bounded correction",
		Repository: delivered.Repository, PublicBranch: delivered.Branch, PRURL: delivered.PRURL,
		PRNumber: delivered.PRNumber, BaseRepository: delivered.BaseRepository,
		BaseBranch: delivered.BaseBranch, BaseSHA: delivered.HeadSHA, AcceptedAt: time.Now().UTC(),
	}
	if err := store.CreateCorrection(correction); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Revise(context.Background(), task.ID, correction.Reason, "different correction", false); err == nil ||
		!strings.Contains(err.Error(), "pending correction intent differs") {
		t.Fatalf("differing pre-pointer intent error = %v", err)
	}
	current, err := store.ReadTask(task.MissionID, task.ID)
	if err != nil || current.CurrentAttempt != 1 || current.CurrentRevision != 1 {
		t.Fatalf("differing intent advanced task pointer: %+v, %v", current, err)
	}
}

func TestReviseExplicitlyAcceptsProvenExternalFastForwardBeforeIntent(t *testing.T) {
	rig, _, task, _, _ := prepareOpenPRForRevision(t)
	externalHead := "3333333333333333333333333333333333333333"
	rig.remote.headSHA = externalHead
	rig.remote.observed.HeadSHA = externalHead
	spawn, err := rig.flow.Revise(context.Background(), task.ID, "reviewed external commit",
		"apply the bounded correction beyond the reviewed public head", true)
	if err != nil {
		t.Fatal(err)
	}
	correction, err := store.ReadCorrection(task.MissionID, task.ID, spawn.Revision)
	if err != nil || !strings.EqualFold(spawn.BaseSHA, externalHead) || !strings.EqualFold(correction.BaseSHA, externalHead) {
		t.Fatalf("accepted external correction base spawn=%+v intent=%+v err=%v", spawn, correction, err)
	}
}

func TestCorrectionRetryStaysWithinRevisionAndReusesExactBase(t *testing.T) {
	rig, _, task, _, _ := prepareOpenPRForRevision(t)
	first, err := rig.flow.Revise(context.Background(), task.ID, "accepted feedback", "bounded correction", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rig.flow.Spawn(context.Background(), task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt != first.Attempt+1 || second.Revision != first.Revision ||
		!strings.EqualFold(second.BaseSHA, first.BaseSHA) {
		t.Fatalf("correction retry first=%+v second=%+v", first, second)
	}
	if len(rig.leases.releases) == 0 {
		t.Fatal("correction retry did not fence the prior exact lease")
	}
}

func prepareVerifiedCorrection(t *testing.T) (*testRig, store.Task, store.Spawn, string) {
	t.Helper()
	rig, _, task, _, _ := prepareOpenPRForRevision(t)
	spawn, err := rig.flow.Revise(context.Background(), task.ID, "accepted feedback", "bounded correction", false)
	if err != nil {
		t.Fatal(err)
	}
	correctionHead := "3333333333333333333333333333333333333333"
	home, _ := datahome.Dir()
	resultPath := writeResult(t, home, spawn, validResult)
	rig.git.headSHA = correctionHead
	if _, err := rig.flow.PublishResult(context.Background(), task.ID, spawn.Attempt, correctionHead, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	return rig, task, spawn, correctionHead
}

func TestCorrectionDeliveryRefusesDriftAndNonDescendantBeforePush(t *testing.T) {
	t.Run("forge identity drift", func(t *testing.T) {
		rig, task, _, _ := prepareVerifiedCorrection(t)
		rig.remote.observed.BaseBranch = "release"
		if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil || !errors.Is(err, ErrReconciliation) {
			t.Fatalf("delivery drift error = %v", err)
		}
		if rig.remote.fastForwards != 0 {
			t.Fatal("drifted correction was pushed")
		}
		pending, err := store.ReadDelivery(task.MissionID, task.ID, 2)
		if err != nil || pending.State != store.DeliveryPending {
			t.Fatalf("pending intent not preserved: %+v, %v", pending, err)
		}
	})
	t.Run("non descendant", func(t *testing.T) {
		rig, task, _, _ := prepareVerifiedCorrection(t)
		rig.delGit.descendantErr = errors.New("not descendant")
		if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil || !strings.Contains(err.Error(), "strictly descend") {
			t.Fatalf("non-descendant delivery error = %v", err)
		}
		if rig.remote.fastForwards != 0 {
			t.Fatal("non-descendant correction was pushed")
		}
		if _, err := store.ReadDelivery(task.MissionID, task.ID, 2); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("non-descendant published delivery intent: %v", err)
		}
	})
	t.Run("public preflight", func(t *testing.T) {
		rig, task, _, _ := prepareVerifiedCorrection(t)
		rig.delGit.messages = []string{"Sophon task task_f0bbc2200213c81f3b03223fb4dc454c correction"}
		if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil || !strings.Contains(err.Error(), "public delivery preflight") {
			t.Fatalf("correction preflight error = %v", err)
		}
		if rig.remote.fastForwards != 0 {
			t.Fatal("unsafe correction was pushed")
		}
	})
}

func TestCorrectionDeliveryRecoversPushBeforeReceiptWithoutDuplicatePush(t *testing.T) {
	rig, task, _, correctionHead := prepareVerifiedCorrection(t)
	rig.remote.fastForwardWriteErr = errors.New("process lost response after push")
	if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil {
		t.Fatal("ambiguous post-push failure was not surfaced")
	}
	pending, err := store.ReadDelivery(task.MissionID, task.ID, 2)
	if err != nil || pending.State != store.DeliveryPending {
		t.Fatalf("post-push pending intent = %+v, %v", pending, err)
	}
	if pending.PRNumber != 17 || pending.PRURL != "https://github.com/acme/repo/pull/17" ||
		pending.PriorHeadSHA == "" || pending.BaseRepository == "" || pending.BaseBranch == "" {
		t.Fatalf("post-push pending intent lacks immutable PR identity: %+v", pending)
	}
	if !strings.EqualFold(rig.remote.headSHA, correctionHead) || rig.remote.fastForwards != 1 {
		t.Fatalf("simulated landed push head=%s calls=%d", rig.remote.headSHA, rig.remote.fastForwards)
	}
	report, err := rig.flow.Status(context.Background())
	if err != nil || len(report.Missions) != 1 || len(report.Missions[0].Tasks) != 1 ||
		report.Missions[0].Tasks[0].State != store.StateCorrectionAwaitingDelivery ||
		!strings.Contains(report.Missions[0].Tasks[0].Detail, "receipt pending recovery") {
		t.Fatalf("post-push recovery status = %+v, %v", report, err)
	}
	rig.remote.fastForwardWriteErr = nil
	receipt, err := rig.flow.Deliver(context.Background(), task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != store.DeliveryDeliveredPR || receipt.PRNumber != 17 || rig.remote.fastForwards != 1 ||
		!receipt.IntentAt.Equal(pending.IntentAt) {
		t.Fatalf("recovered correction receipt=%+v calls=%d pending=%+v", receipt, rig.remote.fastForwards, pending)
	}
	again, err := rig.flow.Deliver(context.Background(), task.ID, true)
	if err != nil || !again.DeliveredAt.Equal(*receipt.DeliveredAt) || rig.remote.fastForwards != 1 {
		t.Fatalf("terminal correction retry=%+v err=%v pushes=%d", again, err, rig.remote.fastForwards)
	}
}

func TestDeliverRefusesPublicBranchCollisionBeforeWrite(t *testing.T) {
	useHome(t)
	rig := newRig()
	task, _ := rig.prepareVerified(t, domain.DeliveryBranch)
	rig.remote.branchExists = true
	rig.remote.headSHA = testBaseSHA

	if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil ||
		!strings.Contains(err.Error(), "already exists at a different head") {
		t.Fatalf("collision error = %v", err)
	}
	if rig.remote.pushes != 0 || rig.remote.creates != 0 {
		t.Fatalf("collision caused external writes: pushes=%d creates=%d", rig.remote.pushes, rig.remote.creates)
	}
	if _, err := store.ReadDelivery(task.MissionID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("collision published intent: %v", err)
	}
}

func TestBranchDeliveryAppliesCommitMessagePreflight(t *testing.T) {
	useHome(t)
	rig := newRig()
	task, _ := rig.prepareVerified(t, domain.DeliveryBranch)
	rig.delGit.messages = []string{"Sophon task task_f0bbc2200213c81f3b03223fb4dc454c attempt 1"}

	if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil ||
		!strings.Contains(err.Error(), "public commit message") {
		t.Fatalf("commit preflight error = %v", err)
	}
	if rig.remote.pushes != 0 || rig.remote.creates != 0 {
		t.Fatalf("preflight caused external writes: pushes=%d creates=%d", rig.remote.pushes, rig.remote.creates)
	}
}

func TestCreateTaskSeparatesLocalIntentFromLaterPublicMetadata(t *testing.T) {
	useHome(t)
	rig := newRig()
	mission, err := rig.flow.CreateMission(context.Background(), "/repo", "Ship", "Mission context")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.CreateTask(context.Background(), mission.ID, "Public title", "", "feature/public", "", "", ""); err == nil {
		t.Fatal("missing detailed objective was accepted")
	}
	local, err := rig.flow.CreateTask(context.Background(), mission.ID, "Local implementation", "Detailed objective", "", "", "", "")
	if err != nil || local.DeliveryMode != domain.DeliveryLocal || local.DeliveryBranch != "" {
		t.Fatalf("local task intake = %+v, %v", local, err)
	}
	if _, err := rig.flow.CreateTask(context.Background(), mission.ID, "Bad\nTitle", "Detailed objective", "feature/public", "", "", ""); err == nil {
		t.Fatal("multiline public title was accepted")
	}
	objective := "Detailed private setup may refer to Sophon, Treehouse, and a local /Users/alice/worktree without becoming public."
	task, err := rig.flow.CreateTask(context.Background(), mission.ID, "HOME-111 Add client", objective,
		"home-111/add-client", "", domain.DeliveryPR, "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "HOME-111 Add client" || task.Objective != objective || task.DeliveryBranch != "home-111/add-client" {
		t.Fatalf("task intent = %+v", task)
	}
}

// TestSupervisorDeathEquivalence: with no supervisor alive, a fresh flow
// instance over the same data home sees the result as ready and verifies it —
// no recovery step exists.
func TestSupervisorDeathEquivalence(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}

	// Simulate supervisor death: a brand-new flow over the same data home.
	fresh := newRig()
	fresh.git.branch = spawn.Branch
	fresh.leaseStatus(spawn)
	report, err := fresh.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missions) != 1 || len(report.Missions[0].Tasks) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if got := report.Missions[0].Tasks[0].State; got != store.StateReady {
		t.Fatalf("state = %q, want ready", got)
	}
	outcome, err := fresh.flow.VerifyComplete(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.HeadSHA != testHeadSHA {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// TestStaleAttemptRefusal: a result published to a fenced attempt is refused
// loudly and current-attempt records are untouched.
func TestStaleAttemptRefusal(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	first, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rig.flow.Spawn(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt != 2 {
		t.Fatalf("second attempt = %+v", second)
	}
	// The retry fenced attempt 1's lease by exact identity.
	if len(rig.leases.releases) != 1 {
		t.Fatalf("releases = %v", rig.leases.releases)
	}
	fenced := rig.leases.released(0)
	if fenced.LeaseID != first.LeaseID || fenced.LeaseHolder != first.LeaseHolder {
		t.Fatalf("fenced release = %+v, want %s/%s", fenced, first.LeaseID, first.LeaseHolder)
	}
	// The worker from attempt 1 finishes late, into its own dir.
	resultPath := writeResult(t, home, first, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(second)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("err = %v, want ErrStaleAttempt", err)
	}
	// Attempt 2 is untouched: no outcome, task still derives active/running.
	if _, err := store.ReadOutcome(task.MissionID, task.ID, 2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("attempt 2 outcome = %v", err)
	}
	status, err := store.Derive(mustTask(t, task.ID))
	if err != nil || status.State != store.StateActive {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

// TestDeliveryCrashWindow: a process dying after PR creation leaves a pending
// intent; re-running converges to delivered_pr with the same PR via
// find-or-create.
func TestDeliveryCrashWindow(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryPR, "")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	// First run: push succeeds, then the process "dies" after the PR was
	// actually created remotely: create reports an error and find sees nothing.
	rig.remote.createErr = errFake
	if _, err := rig.flow.Deliver(ctx, task.ID, true); err == nil {
		t.Fatal("first deliver must fail")
	}
	pending, err := store.ReadDelivery(task.MissionID, task.ID, 1)
	if err != nil || pending.State != store.DeliveryPending {
		t.Fatalf("pending intent = %+v, %v", pending, err)
	}
	// Reality: the PR exists now. Re-running converges without a second create.
	rig.remote.createErr = nil
	rig.remote.pr = &delivery.PullRequest{Repository: testRepo, Branch: task.DeliveryBranch,
		HeadSHA: testHeadSHA, URL: "https://github.com/acme/repo/pull/9", Number: 9}
	receipt, err := rig.flow.Deliver(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != store.DeliveryDeliveredPR || receipt.PRNumber != 9 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !receipt.IntentAt.Equal(pending.IntentAt) {
		t.Fatalf("converged receipt lost original intent time: %+v vs %+v", receipt.IntentAt, pending.IntentAt)
	}
	if rig.remote.creates != 1 {
		t.Fatalf("creates = %d, want 1 (find-or-create converged)", rig.remote.creates)
	}
}

// TestDeliverRequiresPassingValidation: a configured validation command makes
// an absent or failed validation receipt a hard refusal.
func TestDeliverRequiresPassingValidation(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "go test ./...")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	// Absent validation receipt.
	if _, err := rig.flow.Deliver(ctx, task.ID, true); err == nil ||
		!strings.Contains(err.Error(), "validation") {
		t.Fatalf("err = %v", err)
	}
	// A failed validation is a typed result, not an error — but deliver refuses it.
	rig.validate.result = validation.Result{Status: validation.Failed, ExitCode: 1}
	record, err := rig.flow.Validate(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Passed || record.ExitCode != 1 {
		t.Fatalf("record = %+v", record)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); err == nil ||
		!strings.Contains(err.Error(), "validation passed") {
		t.Fatalf("err = %v", err)
	}
	// A passing replacement attempt unblocks delivery while preserving the
	// failed validation receipt on attempt 1.
	second, err := rig.flow.Spawn(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	secondResult := writeResult(t, home, second, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, second.Attempt, testHeadSHA, secondResult); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(second)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	rig.validate.result = validation.Result{Status: validation.Passed, ExitCode: 0}
	if _, err := rig.flow.Validate(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
}

// TestValidateFencesHeadDrift: validation evidence is pinned to the verified head.
func TestValidateFencesHeadDrift(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "go test ./...")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	rig.git.headSHA = "3333333333333333333333333333333333333333"
	rig.git.snapshot = gitcontrol.Snapshot{Head: "3333333333333333333333333333333333333333", Branch: spawn.Branch, Clean: true}
	if _, err := rig.flow.Validate(ctx, task.ID); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("err = %v, want ErrHeadMismatch", err)
	}
	if _, err := store.ReadValidation(task.MissionID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("drifted validation was published: %v", err)
	}
}

// TestStatusIgnoresWakeGarbage: wake lines are notifications, never truth.
func TestStatusIgnoresWakeGarbage(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	// Garbage and contradictions in the wake file change nothing.
	if err := os.WriteFile(store.WakePath(home, task.ID),
		[]byte("delivered: lies\nverified: also lies\n\x00binary garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Missions[0].Tasks[0].State; got != store.StateReady {
		t.Fatalf("state = %q, want ready despite wake garbage", got)
	}
}

// TestStatusLiveAugmentation: active tasks are observed through the pane.
func TestStatusLiveAugmentation(t *testing.T) {
	useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	if _, err := rig.flow.Spawn(ctx, task.ID, false); err != nil {
		t.Fatal(err)
	}
	report, err := rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Missions[0].Tasks[0].State; got != string(herdr.StateRunning) {
		t.Fatalf("state = %q, want running", got)
	}
	rig.panes.observeErr = errFake
	report, err = rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Missions[0].Tasks[0].State; got != "unknown-pane" {
		t.Fatalf("state = %q, want unknown-pane", got)
	}
}

func TestSendWakesWorker(t *testing.T) {
	useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	if _, err := rig.flow.Spawn(ctx, task.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.Send(ctx, task.ID, "please hurry"); err != nil {
		t.Fatal(err)
	}
	if len(rig.panes.wakes) != 1 || rig.panes.wakes[0] != "please hurry" {
		t.Fatalf("wakes = %v", rig.panes.wakes)
	}
}

func TestPublishResultGuards(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Outside the attempt dir.
	outside := filepath.Join(home, "elsewhere.json")
	if err := os.WriteFile(outside, []byte(validResult), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, outside); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("err = %v, want ErrInvalidResult", err)
	}
	// A nonexistent attempt refuses publication.
	if _, err := rig.flow.PublishResult(ctx, task.ID, 9, testHeadSHA, outside); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// A stale file predating the attempt start is refused.
	stale := writeResult(t, home, spawn, validResult)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, stale); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("err = %v, want ErrInvalidResult", err)
	}
	// Even a schema-valid submission cannot become canonical before the live
	// head contract passes.
	valid := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testBaseSHA, valid); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("head mismatch error = %v, want ErrHeadMismatch", err)
	}
	if _, err := store.ReadResult(task.MissionID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("head-rejected submission published canonical result: %v", err)
	}
	// Schema violations are refused: trailing JSON, missing fields, bad paths.
	for name, content := range map[string]string{
		"trailing":  validResult + "\n{}\n",
		"missing":   `{"version":1,"status":"completed","summary":"x"}`,
		"bad_paths": `{"version":1,"status":"completed","summary":"x","verification":[{"command":"t","exit_code":0}],"changed_files":["../escape.go"],"risks":[]}`,
		"bad_check": `{"version":1,"status":"completed","summary":"x","verification":[{"command":"t","exit_code":1}],"changed_files":["a.go"],"risks":[]}`,
	} {
		path := writeResult(t, home, spawn, content)
		if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, path); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("%s: err = %v, want ErrInvalidResult", name, err)
		}
	}
	if _, err := store.ReadResult(task.MissionID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rejected submissions published canonical result: %v", err)
	}
}

func TestPublishTypedReportIsAttentionAndConflictsConservatively(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "go test ./...")
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	path := writeReport(t, home, spawn, store.WorkerReportScopeMismatch, "HOME-111 targets another client", true)
	first, err := rig.flow.PublishReport(ctx, task.ID, 1, testHeadSHA, path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rig.flow.PublishReport(ctx, task.ID, 1, testHeadSHA, path)
	if err != nil || second != first {
		t.Fatalf("idempotent report = %q, %v; want %q", second, err, first)
	}
	report, err := rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Missions[0].Tasks[0]; got.State != store.StateAttention || !strings.Contains(got.Detail, "scope-mismatch") {
		t.Fatalf("report status = %+v, want attention", got)
	}
	if len(report.Actions) != 0 {
		t.Fatalf("typed report yielded automated actions: %+v", report.Actions)
	}
	if _, err := store.ReadResult(task.MissionID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("report pretended completion: %v", err)
	}
	changed := writeReport(t, home, spawn, store.WorkerReportBlocked, "different claim", true)
	if _, err := rig.flow.PublishReport(ctx, task.ID, 1, testHeadSHA, changed); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf("conflicting report error = %v, want evidence conflict", err)
	}
	completion := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, 1, testHeadSHA, completion); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf("report-vs-completion error = %v, want evidence conflict", err)
	}
}

func TestFencedAttemptReportCannotAffectCurrentAttempt(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	first, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Spawn(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
	path := writeReport(t, home, first, store.WorkerReportBlocked, "late report", true)
	if _, err := rig.flow.PublishReport(ctx, task.ID, 1, testHeadSHA, path); err != nil {
		t.Fatal(err)
	}
	report, err := rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Missions[0].Tasks[0]; got.Attempt != 2 || got.State == store.StateAttention || got.State == store.StateReady {
		t.Fatalf("fenced report affected current attempt: %+v", got)
	}
}

func mustTask(t *testing.T, taskID string) store.Task {
	t.Helper()
	task, err := store.FindTask(taskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
