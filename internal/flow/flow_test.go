package flow

import (
	"context"
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
	}
	rig.flow = New(Deps{
		Git: rig.git, Leases: rig.leases, Panes: rig.panes,
		DeliveryGit: rig.delGit, DeliveryRemote: rig.remote,
		NewValidator: func(string) Validator { return rig.validate },
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
	task, err := r.flow.CreateTask(ctx, mission.ID, "Add the feature", "", mode, validationCommand)
	if err != nil {
		t.Fatal(err)
	}
	return mission, task
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
		"`version`, `status`, `summary`, `verification`, `changed_files`, and `risks`"} {
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
	rig.remote.create = delivery.PullRequest{Repository: testRepo, Branch: spawn.Branch,
		HeadSHA: testHeadSHA, URL: "https://github.com/acme/repo/pull/7", Number: 7}
	receipt, err := rig.flow.Deliver(ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != store.DeliveryDeliveredPR || receipt.PRNumber != 7 ||
		receipt.PRURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("receipt = %+v", receipt)
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
	rig.remote.pr = &delivery.PullRequest{Repository: testRepo, Branch: spawn.Branch,
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
	// A passing run unblocks delivery.
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
