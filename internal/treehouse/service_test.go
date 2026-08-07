package treehouse

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeCLI struct {
	allocation   Allocation
	statuses     []WorktreeStatus
	acquireCalls int
	releases     []Allocation
	statusCalls  int
	acquireErr   error
	releaseErr   error
	statusErr    error
}

func (f *fakeCLI) Acquire(_ context.Context, _ string, holder string) (Allocation, error) {
	f.acquireCalls++
	if f.acquireErr != nil {
		return Allocation{}, f.acquireErr
	}
	allocation := f.allocation
	allocation.LeaseHolder = holder
	return allocation, nil
}

func (f *fakeCLI) Release(_ context.Context, _ string, lease Allocation) error {
	f.releases = append(f.releases, lease)
	return f.releaseErr
}

func (f *fakeCLI) Status(_ context.Context, _ string) ([]WorktreeStatus, error) {
	f.statusCalls++
	return f.statuses, f.statusErr
}

type fakeGit struct {
	snapshot gitcontrol.Snapshot
	err      error
	branches map[string]bool
}

func (f fakeGit) CreateTaskBranch(_ context.Context, _ string, branch string) (gitcontrol.Snapshot, error) {
	if f.branches != nil {
		if f.branches[branch] {
			return gitcontrol.Snapshot{}, errors.New("branch already exists")
		}
		f.branches[branch] = true
	}
	f.snapshot.Branch = branch
	return f.snapshot, f.err
}

func (f fakeGit) Snapshot(context.Context, string) (gitcontrol.Snapshot, error) {
	return f.snapshot, f.err
}

func TestAcquirePersistsAndReacquireReusesOneLease(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/one", LeaseID: "lease-one"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "task-one", Clean: true}})

	first, err := service.Acquire(ctx, "cmd_acquire_one", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseHolder != LeaseHolder(task.ID, 1) || first.BaseSHA != testSHA ||
		first.Branch != TaskBranch(task.Title, task.ID, 1) || first.Project != "project" || first.State != domain.TreehouseLeaseActive {
		t.Fatalf("acquired lease = %+v", first)
	}
	attempt, err := store.Attempt(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.TreehouseLeaseID != first.LeaseID || attempt.TreehouseLeaseHolder != first.LeaseHolder ||
		attempt.WorktreePath != first.WorktreePath || attempt.BaseSHA != testSHA || attempt.Branch != TaskBranch(task.Title, task.ID, 1) {
		t.Fatalf("persisted attempt = %+v", attempt)
	}

	current, err := store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, "cmd_start_after_acquire", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: current.State, ExpectedVersion: current.Version,
		To: domain.TaskStarting, Actor: "test",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := service.Acquire(ctx, "cmd_acquire_again", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.LeaseID != first.LeaseID || cli.acquireCalls != 1 {
		t.Fatalf("reacquire = %+v, acquire calls = %d", second, cli.acquireCalls)
	}
	leases, err := store.ActiveTreehouseLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 {
		t.Fatalf("active leases = %+v, want exactly one", leases)
	}
}

func TestTaskBranchUsesReadableTaskName(t *testing.T) {
	if got := TaskBranch("Fix concurrent invitation acceptance", "a2e2b9", 1); got != "pintellect/fix-concurrent-invitation-acceptance-a2e2b9/attempt-1" {
		t.Fatalf("task branch = %q", got)
	}
}

func TestRetryAcquiresNewAttemptLeaseAndFencesOldAttempt(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/one", LeaseID: "lease-one"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Clean: true}, branches: map[string]bool{}})
	first, err := service.Acquire(ctx, "cmd_acquire_attempt_one", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.TransitionTask(ctx, "cmd_fail_attempt_one", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: current.State, ExpectedVersion: current.Version,
		To: domain.TaskFailed, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryTask(ctx, "cmd_retry_attempt_two", db.RetryTaskInput{
		TaskID: task.ID, ExpectedVersion: failed.Version, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTask(ctx, "cmd_provision_attempt_two", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: retried.CurrentAttempt, ExpectedState: retried.State, ExpectedVersion: retried.Version,
		To: domain.TaskProvisioning, Actor: "test",
	}); err != nil {
		t.Fatal(err)
	}
	cli.allocation = Allocation{WorktreePath: "/worktrees/two", LeaseID: "lease-two"}
	second, err := service.Acquire(ctx, "cmd_acquire_attempt_two", task.ID, retried.CurrentAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt != 2 || second.LeaseID == first.LeaseID || second.LeaseHolder == first.LeaseHolder ||
		second.Branch != TaskBranch(task.Title, task.ID, 2) || second.BaseSHA != testSHA {
		t.Fatalf("retry lease = %+v; first = %+v", second, first)
	}
	if _, err := service.Release(ctx, "cmd_stale_release", task.ID, 1); !errors.Is(err, db.ErrStaleAttempt) {
		t.Fatalf("stale release error = %v, want ErrStaleAttempt", err)
	}
	if len(cli.releases) != 0 {
		t.Fatalf("stale release invoked Treehouse: %+v", cli.releases)
	}
}

func TestStaleAttemptCannotReleaseCurrentAttemptLease(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/attempt-one", LeaseID: "lease-one"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt-one", Clean: true}})
	if _, err := service.Acquire(ctx, "cmd_acquire_attempt_one", task.ID, 1); err != nil {
		t.Fatal(err)
	}
	current, err := store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.TransitionTask(ctx, "cmd_fail_attempt_one", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: current.State, ExpectedVersion: current.Version,
		To: domain.TaskFailed, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryTask(ctx, "cmd_retry_attempt_two", db.RetryTaskInput{
		TaskID: task.ID, ExpectedVersion: failed.Version, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retried.CurrentAttempt != 2 {
		t.Fatalf("current attempt = %d, want 2", retried.CurrentAttempt)
	}
	if _, err := service.Release(ctx, "cmd_stale_release", task.ID, 1); !errors.Is(err, db.ErrStaleAttempt) {
		t.Fatalf("stale release error = %v, want ErrStaleAttempt", err)
	}
	if len(cli.releases) != 0 {
		t.Fatalf("stale release invoked Treehouse: %+v", cli.releases)
	}
}

func TestReleaseUsesPersistedIdentityAndMarksLeaseReleased(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/release", LeaseID: "release-lease"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt", Clean: true}})
	acquired, err := service.Acquire(ctx, "cmd_acquire_release", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	released, err := service.Release(ctx, "cmd_release", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if released.State != domain.TreehouseLeaseReleased || released.ReleasedAt == nil {
		t.Fatalf("released lease = %+v", released)
	}
	if len(cli.releases) != 1 || cli.releases[0].LeaseID != acquired.LeaseID ||
		cli.releases[0].LeaseHolder != acquired.LeaseHolder || cli.releases[0].WorktreePath != acquired.WorktreePath {
		t.Fatalf("external release = %+v, acquired = %+v", cli.releases, acquired)
	}
	persisted, err := store.TreehouseLease(ctx, task.ID, 1)
	if err != nil || persisted.State != domain.TreehouseLeaseReleased {
		t.Fatalf("persisted released lease = %+v, %v", persisted, err)
	}
}

func TestReconcileKeepsMatchingLeaseValid(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/valid", LeaseID: "valid-lease"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt", Clean: true}})
	lease, err := service.Acquire(ctx, "cmd_acquire_valid", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	cli.statuses = []WorktreeStatus{{WorktreePath: lease.WorktreePath, Status: "leased",
		LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder}}
	result, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid != 1 || result.Fenced != 0 || result.Missing != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
	persisted, err := store.TreehouseLease(ctx, task.ID, 1)
	if err != nil || persisted.State != domain.TreehouseLeaseActive {
		t.Fatalf("valid lease changed = %+v, %v", persisted, err)
	}
}

func TestReconcileAdoptsLeaseAcquiredBeforeDatabaseRecord(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	leasedAt := time.Unix(42, 0).UTC()
	branch := TaskBranch(task.Title, task.ID, 1)
	cli := &fakeCLI{statuses: []WorktreeStatus{{WorktreePath: "/worktrees/unrecorded",
		Status: "leased", LeaseID: "lease-unrecorded", LeaseHolder: LeaseHolder(task.ID, 1),
		LeasedAt: &leasedAt}}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{
		Head: testSHA, Branch: branch, Clean: true,
	}})

	result, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Adopted != 1 || result.Awaiting != 0 || cli.acquireCalls != 0 || len(cli.releases) != 0 {
		t.Fatalf("reconcile result=%+v acquire=%d releases=%+v", result, cli.acquireCalls, cli.releases)
	}
	lease, err := store.TreehouseLease(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if lease.LeaseID != "lease-unrecorded" || lease.LeaseHolder != LeaseHolder(task.ID, 1) ||
		lease.Branch != branch || lease.BaseSHA != testSHA || !lease.AcquiredAt.Equal(leasedAt) {
		t.Fatalf("adopted lease = %+v", lease)
	}
	attempt, err := store.Attempt(ctx, task.ID, 1)
	if err != nil || attempt.TreehouseLeaseID != lease.LeaseID || attempt.WorktreePath != lease.WorktreePath {
		t.Fatalf("adopted attempt=%+v err=%v", attempt, err)
	}
}

func TestReconcileReportsProvisioningTaskStillAwaitingLease(t *testing.T) {
	store, _ := provisioningTask(t)
	defer store.Close()
	service := NewService(store, &fakeCLI{}, fakeGit{})
	result, err := service.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Awaiting != 1 || result.Adopted != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
}

func TestLeaseMismatchFencesAttemptWithoutTouchingNewHolder(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/reused", LeaseID: "old-lease"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt", Clean: true}})
	lease, err := service.Acquire(ctx, "cmd_acquire_mismatch", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	cli.statuses = []WorktreeStatus{{
		WorktreePath: lease.WorktreePath, Status: "leased",
		LeaseID: "new-lease", LeaseHolder: "parallel-intellect:other-task:1",
	}}
	result, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fenced != 1 || result.Valid != 0 || result.Missing != 0 {
		t.Fatalf("reconcile result = %+v", result)
	}
	if len(cli.releases) != 0 {
		t.Fatalf("mismatch touched new holder through release: %+v", cli.releases)
	}
	persisted, err := store.TreehouseLease(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != domain.TreehouseLeaseFenced {
		t.Fatalf("lease state = %s, want fenced", persisted.State)
	}
	failed, err := store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != domain.TaskFailed {
		t.Fatalf("task state = %s, want failed", failed.State)
	}
}

func TestMissingWorktreeFailsCurrentAttempt(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/missing", LeaseID: "missing-lease"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt", Clean: true}})
	if _, err := service.Acquire(ctx, "cmd_acquire_missing", task.ID, 1); err != nil {
		t.Fatal(err)
	}
	result, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Missing != 1 {
		t.Fatalf("reconcile result = %+v", result)
	}
	lease, err := store.TreehouseLease(ctx, task.ID, 1)
	if err != nil || lease.State != domain.TreehouseLeaseMissing {
		t.Fatalf("missing lease = %+v, %v", lease, err)
	}
	current, err := store.Task(ctx, task.ID)
	if err != nil || current.State != domain.TaskFailed {
		t.Fatalf("task after missing worktree = %+v, %v", current, err)
	}
}

func TestCommandClientReleaseAlwaysUsesLeaseGuards(t *testing.T) {
	runner := &recordingRunner{}
	client := &CommandClient{runner: runner}
	lease := Allocation{WorktreePath: "/worktrees/one", LeaseID: "lease-id", LeaseHolder: "holder-id"}
	if err := client.Release(context.Background(), "/project", lease); err != nil {
		t.Fatal(err)
	}
	want := []string{"return", "--force", "--if-lease-id", "lease-id", "--if-lease-holder", "holder-id", "/worktrees/one"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("release args = %+v, want %+v", runner.calls, want)
	}
	if reflect.DeepEqual(runner.calls[0], []string{"return", "/worktrees/one"}) {
		t.Fatal("release used path alone")
	}
}

func TestCommandClientAcquireUsesDurableLeaseContract(t *testing.T) {
	runner := &recordingRunner{stdout: []byte(`{"path":"/worktrees/one","lease_id":"lease-id","lease_holder":"holder-id","leased_at":"2026-08-06T15:00:00-05:00"}`)}
	client := &CommandClient{runner: runner}
	lease, err := client.Acquire(context.Background(), "/project", "holder-id")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"get", "--lease", "--lease-holder", "holder-id", "--json"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("acquire args = %+v, want %+v", runner.calls, want)
	}
	if lease.WorktreePath != "/worktrees/one" || lease.LeaseID != "lease-id" || lease.LeaseHolder != "holder-id" {
		t.Fatalf("lease = %+v", lease)
	}
}

func TestReleaseReconcilesCrashAfterExternalConditionalReturn(t *testing.T) {
	ctx := context.Background()
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/released", LeaseID: "lease-release-crash"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Clean: true}})
	lease, err := service.Acquire(ctx, "cmd_acquire_release_crash", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	cli.releaseErr = errors.New("conditional return reports lease already gone")
	cli.statuses = []WorktreeStatus{{WorktreePath: lease.WorktreePath, Status: "available"}}
	released, err := service.Release(ctx, "cmd_release_after_crash", task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if released.State != domain.TreehouseLeaseReleased || len(cli.releases) != 1 || cli.statusCalls != 1 {
		t.Fatalf("released=%+v releases=%+v statusCalls=%d", released, cli.releases, cli.statusCalls)
	}
}

func TestAcquireFailureAfterAllocationUsesGuardedCompensation(t *testing.T) {
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/dirty", LeaseID: "dirty-lease"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt", Clean: false}})
	if _, err := service.Acquire(context.Background(), "cmd_dirty_acquire", task.ID, 1); err == nil {
		t.Fatal("dirty acquisition unexpectedly succeeded")
	}
	if len(cli.releases) != 1 {
		t.Fatalf("compensation releases = %+v", cli.releases)
	}
	released := cli.releases[0]
	if released.LeaseID != "dirty-lease" || released.LeaseHolder != LeaseHolder(task.ID, 1) ||
		released.WorktreePath != "/worktrees/dirty" {
		t.Fatalf("compensation omitted lease guards: %+v", released)
	}
}

func TestAcquireRejectsUnexpectedHolderAndReturnsExactAllocation(t *testing.T) {
	store, task := provisioningTask(t)
	defer store.Close()
	runner := &recordingRunner{stdout: []byte(`{"path":"/worktrees/unexpected","lease_id":"unexpected-lease","lease_holder":"unexpected-holder","leased_at":"2026-08-06T15:00:00-05:00"}`)}
	service := NewService(store, &CommandClient{runner: runner}, fakeGit{})
	if _, err := service.Acquire(context.Background(), "cmd_unexpected_holder", task.ID, 1); err == nil {
		t.Fatal("unexpected holder acquisition succeeded")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("Treehouse calls = %+v, want acquire and guarded return", runner.calls)
	}
	wantReturn := []string{"return", "--force", "--if-lease-id", "unexpected-lease",
		"--if-lease-holder", "unexpected-holder", "/worktrees/unexpected"}
	if !reflect.DeepEqual(runner.calls[1], wantReturn) {
		t.Fatalf("compensation args = %+v, want %+v", runner.calls[1], wantReturn)
	}
}

func TestReleaseValidatesCommandBeforeExternalCall(t *testing.T) {
	store, task := provisioningTask(t)
	defer store.Close()
	cli := &fakeCLI{allocation: Allocation{WorktreePath: "/worktrees/one", LeaseID: "lease-one"}}
	service := NewService(store, cli, fakeGit{snapshot: gitcontrol.Snapshot{Head: testSHA, Branch: "attempt", Clean: true}})
	if _, err := service.Acquire(context.Background(), "cmd_acquire_before_release", task.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(context.Background(), "", task.ID, 1); err == nil {
		t.Fatal("release without command ID succeeded")
	}
	if len(cli.releases) != 0 {
		t.Fatalf("invalid release invoked Treehouse: %+v", cli.releases)
	}
}

type recordingRunner struct {
	calls  [][]string
	stdout []byte
	stderr []byte
	err    error
}

func (r *recordingRunner) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.stdout, r.stderr, r.err
}

func TestRealTreehouseCLILeaseSmoke(t *testing.T) {
	if os.Getenv("PARALLEL_INTELLECT_TREEHOUSE_SMOKE") != "1" {
		t.Skip("set PARALLEL_INTELLECT_TREEHOUSE_SMOKE=1 to exercise the installed treehouse CLI")
	}
	ctx := context.Background()
	projectBytes, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	projectPath := strings.TrimSpace(string(projectBytes))
	holder := "parallel-intellect:real-cli-smoke:" + strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "-")
	client := NewCommandClient("treehouse")
	lease, err := client.Acquire(ctx, projectPath, holder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Release(context.Background(), projectPath, lease); err != nil {
			t.Errorf("conditionally return smoke lease: %v", err)
		}
	})
	statuses, err := client.Status(ctx, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, status := range statuses {
		if status.WorktreePath == lease.WorktreePath && status.LeaseID == lease.LeaseID && status.LeaseHolder == holder {
			found = true
		}
	}
	if !found {
		t.Fatalf("acquired lease not present in status: %+v", statuses)
	}
}

func provisioningTask(t *testing.T) (*db.Store, domain.Task) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, domain.CommandID("cmd_project_"+t.Name()), db.CreateProjectInput{
		Name: "project", Path: "/registered/project",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, domain.CommandID("cmd_mission_"+t.Name()), db.CreateMissionInput{
		ProjectID: projectID, Title: "mission", Objective: "objective",
		Budget: domain.MissionBudget{MaxTaskAttempts: 3},
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, domain.CommandID("cmd_task_"+t.Name()), db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "task",
		Objective: "objective", DeliveryMode: domain.DeliveryGate,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, err = store.TransitionTask(ctx, domain.CommandID("cmd_provision_"+t.Name()), db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
		To: domain.TaskProvisioning, Actor: "test",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, task
}
