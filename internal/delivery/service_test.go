package delivery_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/delivery"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/treehouse"
)

const (
	testBaseSHA = "1111111111111111111111111111111111111111"
	testHeadSHA = "2222222222222222222222222222222222222222"
)

type fakeLocalGit struct {
	head       string
	branch     string
	repository string
	checks     int
}

func (f *fakeLocalGit) VerifyHead(_ context.Context, _, branch, head string) error {
	f.checks++
	if f.branch != branch {
		return delivery.ErrBranchMismatch
	}
	if !strings.EqualFold(f.head, head) {
		return delivery.ErrHeadMismatch
	}
	return nil
}

func (f *fakeLocalGit) Repository(context.Context, string) (string, error) {
	return f.repository, nil
}

type fakeRemote struct {
	remoteHead       string
	pull             *delivery.PullRequest
	pushes           int
	finds            int
	creates          int
	crashAfterCreate bool
}

func (f *fakeRemote) Push(_ context.Context, repository, _, branch, head string) error {
	f.pushes++
	f.remoteHead = head
	if f.pull != nil {
		f.pull.Repository = repository
		f.pull.Branch = branch
		f.pull.HeadSHA = head
	}
	return nil
}

func (f *fakeRemote) FindPullRequest(_ context.Context, repository, _, branch, head string) (*delivery.PullRequest, error) {
	f.finds++
	if f.pull == nil || f.pull.Repository != repository || f.pull.Branch != branch || !strings.EqualFold(f.pull.HeadSHA, head) {
		return nil, nil
	}
	copy := *f.pull
	return &copy, nil
}

func (f *fakeRemote) CreatePullRequest(_ context.Context, in delivery.PullRequestInput) (delivery.PullRequest, error) {
	f.creates++
	created := delivery.PullRequest{Repository: in.Repository, Branch: in.Branch, HeadSHA: in.HeadSHA,
		URL: "https://example.invalid/repo/pull/17", Number: 17}
	f.pull = &created
	if f.crashAfterCreate {
		return delivery.PullRequest{}, errors.New("injected crash after pull request creation")
	}
	return created, nil
}

func (f *fakeRemote) HeadSHA(context.Context, string, string, string) (string, error) {
	return f.remoteHead, nil
}

type fakeGate struct {
	result delivery.GateResult
	calls  int
	err    error
}

func (f *fakeGate) Run(context.Context, string, string) (delivery.GateResult, error) {
	f.calls++
	return f.result, f.err
}

func TestDeliveryIdempotencySameCommandReturnsSamePR(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryPR)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{}
	service := delivery.Service{Store: store, Git: local, Remote: remote}
	request := delivery.Request{TaskID: task.ID, CommandID: "cmd_delivery_same", Actor: "test"}

	first, err := service.Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated command changed result:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if remote.creates != 1 || remote.pushes != 1 || first.Delivery.HeadSHA != testHeadSHA ||
		first.Task.State != domain.TaskDelivered {
		t.Fatalf("remote creates=%d pushes=%d result=%+v", remote.creates, remote.pushes, first)
	}
}

func TestCrashAfterPRCreationReconcilesWithoutDuplicate(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryPR)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{crashAfterCreate: true}
	service := delivery.Service{Store: store, Git: local, Remote: remote}
	request := delivery.Request{TaskID: task.ID, CommandID: "cmd_delivery_crash", Actor: "test"}

	if _, err := service.Deliver(context.Background(), request); err == nil {
		t.Fatal("injected post-create crash unexpectedly succeeded")
	}
	pending, err := store.Delivery(context.Background(), task.ID, 1)
	if err != nil || pending == nil || pending.State != delivery.StatePending || remote.creates != 1 {
		t.Fatalf("pending=%+v err=%v creates=%d", pending, err, remote.creates)
	}
	remote.crashAfterCreate = false
	result, err := service.Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if remote.creates != 1 || remote.finds != 2 || result.Delivery.PRNumber != 17 ||
		result.Delivery.State != delivery.StateDelivered {
		t.Fatalf("reconciled result=%+v creates=%d finds=%d", result, remote.creates, remote.finds)
	}
}

func TestStartupReconcileReplaysPersistedDeliveryInputs(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryPR)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{crashAfterCreate: true}
	service := delivery.Service{Store: store, Git: local, Remote: remote}
	request := delivery.Request{TaskID: task.ID, CommandID: "cmd_delivery_restart",
		Base: "release/v1", Actor: "commander"}
	if _, err := service.Deliver(context.Background(), request); err == nil {
		t.Fatal("injected post-create crash unexpectedly succeeded")
	}
	record, err := store.Delivery(context.Background(), task.ID, 1)
	if err != nil || record == nil || record.RequestBase != request.Base || record.RequestActor != request.Actor {
		t.Fatalf("persisted recovery request=%+v err=%v", record, err)
	}
	remote.crashAfterCreate = false
	result, err := service.Reconcile(context.Background(), task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if remote.creates != 1 || result.Task.State != domain.TaskDelivered || result.Delivery.PRNumber != 17 {
		t.Fatalf("reconciled result=%+v creates=%d", result, remote.creates)
	}
}

func TestSHAMismatchBlocksBeforeDeliveryMutation(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryPR)
	defer store.Close()
	local := &fakeLocalGit{head: testBaseSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{}
	service := delivery.Service{Store: store, Git: local, Remote: remote}

	_, err := service.Deliver(context.Background(), delivery.Request{
		TaskID: task.ID, CommandID: "cmd_delivery_mismatch", Actor: "test",
	})
	if !errors.Is(err, delivery.ErrHeadMismatch) {
		t.Fatalf("delivery error = %v, want ErrHeadMismatch", err)
	}
	record, lookupErr := store.Delivery(context.Background(), task.ID, 1)
	current, taskErr := store.Task(context.Background(), task.ID)
	if lookupErr != nil || taskErr != nil || record != nil || current.State != domain.TaskReady || remote.pushes != 0 || remote.creates != 0 {
		t.Fatalf("record=%+v lookup=%v task=%+v taskErr=%v remote=%+v", record, lookupErr, current, taskErr, remote)
	}
}

func TestBranchModeRetainsLease(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryBranch)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch}
	service := delivery.Service{Store: store, Git: local}

	result, err := service.Deliver(context.Background(), delivery.Request{
		TaskID: task.ID, CommandID: "cmd_delivery_branch", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.TreehouseLease(context.Background(), task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.State != domain.TaskDeliveredBranch || result.Delivery.State != delivery.StateDeliveredBranch ||
		lease.State != domain.TreehouseLeaseActive || lease.ReleasedAt != nil {
		t.Fatalf("result=%+v lease=%+v", result, lease)
	}
}

func TestNoMistakesGateRunsBeforePRCreation(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryGate)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{}
	gate := &fakeGate{result: delivery.GateResult{Passed: true, Output: "outcome: passed"}}
	service := delivery.Service{Store: store, Git: local, Remote: remote, Gate: gate}

	result, err := service.Deliver(context.Background(), delivery.Request{
		TaskID: task.ID, CommandID: "cmd_delivery_gate", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gate.calls != 1 || local.checks != 2 || remote.creates != 1 || result.Delivery.GateState != delivery.GatePassed ||
		result.Task.State != domain.TaskDelivered {
		t.Fatalf("gate calls=%d git checks=%d creates=%d result=%+v", gate.calls, local.checks, remote.creates, result)
	}
}

func TestFailedGateIsIdempotentAndNewCommandCanRetry(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryGate)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{}
	gate := &fakeGate{result: delivery.GateResult{Passed: false, Output: "outcome: failed"}}
	service := delivery.Service{Store: store, Git: local, Remote: remote, Gate: gate}
	request := delivery.Request{TaskID: task.ID, CommandID: "cmd_failed_gate", Actor: "test"}

	first, err := service.Deliver(context.Background(), request)
	if !errors.Is(err, delivery.ErrGateFailed) {
		t.Fatalf("first gate error = %v", err)
	}
	second, err := service.Deliver(context.Background(), request)
	if !errors.Is(err, delivery.ErrGateFailed) || !reflect.DeepEqual(first, second) || gate.calls != 1 {
		t.Fatalf("repeat result=%+v err=%v calls=%d; first=%+v", second, err, gate.calls, first)
	}
	gate.result = delivery.GateResult{Passed: true, Output: "outcome: passed"}
	result, err := service.Deliver(context.Background(), delivery.Request{
		TaskID: task.ID, CommandID: "cmd_retry_gate", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gate.calls != 2 || result.Task.State != domain.TaskDelivered || result.Delivery.GateState != delivery.GatePassed {
		t.Fatalf("retry result=%+v calls=%d", result, gate.calls)
	}
}

func TestStartupDoesNotSilentlyRepeatInterruptedNoMistakesGate(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryGate)
	defer store.Close()
	local := &fakeLocalGit{head: testHeadSHA, branch: branch, repository: "git@example.invalid/repo.git"}
	remote := &fakeRemote{}
	gate := &fakeGate{err: errors.New("injected crash during no-mistakes")}
	service := delivery.Service{Store: store, Git: local, Remote: remote, Gate: gate}
	if _, err := service.Deliver(context.Background(), delivery.Request{
		TaskID: task.ID, CommandID: "cmd_gate_crash", Actor: "test",
	}); err == nil {
		t.Fatal("injected gate crash unexpectedly succeeded")
	}
	result, err := service.Reconcile(context.Background(), task.ID, 1)
	if !errors.Is(err, delivery.ErrGateRecoveryRequired) || gate.calls != 1 ||
		result.Delivery.GateState != delivery.GatePending || result.Task.State != domain.TaskValidating {
		t.Fatalf("reconcile result=%+v err=%v gateCalls=%d", result, err, gate.calls)
	}
}

type releaseCLI struct {
	releases   []treehouse.Allocation
	statuses   []treehouse.WorktreeStatus
	releaseErr error
}

func (*releaseCLI) Acquire(context.Context, string, string) (treehouse.Allocation, error) {
	return treehouse.Allocation{}, errors.New("unexpected acquire")
}

func (f *releaseCLI) Release(_ context.Context, _ string, allocation treehouse.Allocation) error {
	f.releases = append(f.releases, allocation)
	return f.releaseErr
}

func (f *releaseCLI) Status(context.Context, string) ([]treehouse.WorktreeStatus, error) {
	return f.statuses, nil
}

type unusedGitInspector struct{}

func (unusedGitInspector) CreateTaskBranch(context.Context, string, string) (gitcontrol.Snapshot, error) {
	return gitcontrol.Snapshot{}, errors.New("unexpected task branch creation")
}

func (unusedGitInspector) Snapshot(context.Context, string) (gitcontrol.Snapshot, error) {
	return gitcontrol.Snapshot{}, errors.New("unexpected task snapshot")
}

func TestReleaseUsesConditionalM2LeasePath(t *testing.T) {
	store, task, branch := readyTask(t, domain.DeliveryBranch)
	defer store.Close()
	service := delivery.Service{Store: store, Git: &fakeLocalGit{head: testHeadSHA, branch: branch}}
	if _, err := service.Deliver(context.Background(), delivery.Request{
		TaskID: task.ID, CommandID: "cmd_delivery_before_release", Actor: "test",
	}); err != nil {
		t.Fatal(err)
	}
	cli := &releaseCLI{}
	service.Leases = treehouse.NewService(store, cli, unusedGitInspector{})
	released, err := service.Release(context.Background(), task.ID, "cmd_delivery_release", "test")
	if err != nil {
		t.Fatal(err)
	}
	if released.State != domain.TreehouseLeaseReleased || len(cli.releases) != 1 ||
		cli.releases[0].LeaseID != "lease-delivery" || cli.releases[0].LeaseHolder != "holder-delivery" ||
		cli.releases[0].WorktreePath == "" {
		t.Fatalf("released=%+v external conditional releases=%+v", released, cli.releases)
	}
	if _, err := service.Release(context.Background(), task.ID, "cmd_delivery_release_retry", "test"); err != nil {
		t.Fatal(err)
	}
	if len(cli.releases) != 1 {
		t.Fatalf("idempotent release repeated external return: %+v", cli.releases)
	}
}

func TestStartupFinishesReleaseThatCompletedBeforeDatabaseRecord(t *testing.T) {
	ctx := context.Background()
	store, task, branch := readyTask(t, domain.DeliveryBranch)
	defer store.Close()
	service := delivery.Service{Store: store, Git: &fakeLocalGit{head: testHeadSHA, branch: branch}}
	if _, err := service.Deliver(ctx, delivery.Request{TaskID: task.ID,
		CommandID: "cmd_delivery_before_release_crash", Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	releaseCommand := domain.CommandID("cmd_release_crash")
	reservation, err := store.ReserveDelivery(ctx, releaseCommand, delivery.ReserveInput{
		TaskID: task.ID, Operation: "release", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.TreehouseLease(ctx, task.ID, reservation.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	intent := delivery.ReleaseIntentInput{TaskID: task.ID, Attempt: reservation.Attempt,
		LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder,
		RequestCommandID: releaseCommand, Actor: "test"}
	if err := store.PrepareDeliveryRelease(ctx, "cmd_release_crash:delivery:release-prepare", intent); err != nil {
		t.Fatal(err)
	}

	// Model a process death after Treehouse accepted the guarded return: the
	// worktree is visible but no longer carries our lease identity.
	cli := &releaseCLI{statuses: []treehouse.WorktreeStatus{{WorktreePath: lease.WorktreePath, Status: "available"}}}
	leaseService := treehouse.NewService(store, cli, unusedGitInspector{})
	reconciled, err := leaseService.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Released != 1 || len(cli.releases) != 0 {
		t.Fatalf("reconciled=%+v external releases=%+v", reconciled, cli.releases)
	}
	record, err := store.Delivery(ctx, task.ID, reservation.Attempt)
	if err != nil || record == nil || record.ReleaseState != "completed" {
		t.Fatalf("startup did not complete release record=%+v err=%v", record, err)
	}

	service = delivery.Service{Store: store, Leases: leaseService}
	released, err := service.Release(ctx, task.ID, "cmd_release_after_restart", "test")
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Delivery(ctx, task.ID, reservation.Attempt)
	if err != nil || record == nil || record.ReleaseState != "completed" ||
		released.State != domain.TreehouseLeaseReleased || len(cli.releases) != 0 {
		t.Fatalf("record=%+v released=%+v external=%+v err=%v", record, released, cli.releases, err)
	}
}

func TestStartupResumesReleaseIntentBeforeExternalReturn(t *testing.T) {
	ctx := context.Background()
	store, task, branch := readyTask(t, domain.DeliveryBranch)
	defer store.Close()
	service := delivery.Service{Store: store, Git: &fakeLocalGit{head: testHeadSHA, branch: branch}}
	if _, err := service.Deliver(ctx, delivery.Request{TaskID: task.ID,
		CommandID: "cmd_delivery_before_pending_release", Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	commandID := domain.CommandID("cmd_pending_release")
	reservation, err := store.ReserveDelivery(ctx, commandID, delivery.ReserveInput{
		TaskID: task.ID, Operation: "release", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.TreehouseLease(ctx, task.ID, reservation.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	intent := delivery.ReleaseIntentInput{TaskID: task.ID, Attempt: reservation.Attempt,
		LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder, RequestCommandID: commandID, Actor: "test"}
	if err := store.PrepareDeliveryRelease(ctx, "cmd_pending_release:delivery:release-prepare", intent); err != nil {
		t.Fatal(err)
	}
	cli := &releaseCLI{statuses: []treehouse.WorktreeStatus{{WorktreePath: lease.WorktreePath,
		Status: "leased", LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder}}}
	leaseService := treehouse.NewService(store, cli, unusedGitInspector{})
	reconciled, err := leaseService.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Delivery(ctx, task.ID, reservation.Attempt)
	if err != nil || reconciled.Released != 1 || len(cli.releases) != 1 ||
		record == nil || record.ReleaseState != "completed" {
		t.Fatalf("reconciled=%+v record=%+v external=%+v err=%v", reconciled, record, cli.releases, err)
	}
}

func readyTask(t *testing.T, mode domain.DeliveryMode) (*db.Store, domain.Task, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	store, err := db.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, "cmd_project", db.CreateProjectInput{Name: "project", Path: root})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_mission", db.CreateMissionInput{
		ProjectID: projectID, Title: "Mission", Objective: "Deliver safely",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	branch := "pintellect/test/attempt-1"
	task, err := store.CreateTask(ctx, "cmd_task", db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "Delivery task",
		Objective: "Create exactly one pull request", DeliveryMode: mode, Branch: branch,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, err = store.TransitionTask(ctx, "cmd_provision", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
		To: domain.TaskProvisioning, Actor: "test",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	_, err = store.RecordTreehouseLease(ctx, "cmd_lease", db.RecordTreehouseLeaseInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, Actor: "test",
		Lease: domain.TreehouseLease{LeaseID: "lease-delivery", LeaseHolder: "holder-delivery",
			WorktreePath: worktree, Project: "project", Branch: branch, BaseSHA: testBaseSHA,
			AcquiredAt: time.Unix(1, 0).UTC()},
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	for _, state := range []domain.TaskState{domain.TaskStarting, domain.TaskRunning} {
		task, err = store.TransitionTask(ctx, domain.CommandID("cmd_"+string(state)), db.TransitionTaskInput{
			TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
			To: state, Actor: "test",
		})
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	task, err = store.CompleteWorkerTask(ctx, "cmd_complete", db.CompleteWorkerTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, LeaseID: "lease-delivery",
		LeaseHolder: "holder-delivery", HeadSHA: testHeadSHA, ResultPath: "/result.json",
		ResultSHA256: "result-hash", Actor: "worker", Result: domain.WorkerResult{
			Version: 1, Status: "completed", Summary: "done",
			Verification: []domain.VerificationResult{{Command: "test", ExitCode: 0}},
			ChangedFiles: []string{"file.go"}, Risks: []string{},
		},
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, task, branch
}
