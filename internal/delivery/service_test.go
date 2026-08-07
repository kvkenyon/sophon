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
}

func (f *fakeGate) Run(context.Context, string, string) (delivery.GateResult, error) {
	f.calls++
	return f.result, nil
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

type releaseCLI struct {
	releases []treehouse.Allocation
}

func (*releaseCLI) Acquire(context.Context, string, string) (treehouse.Allocation, error) {
	return treehouse.Allocation{}, errors.New("unexpected acquire")
}

func (f *releaseCLI) Release(_ context.Context, _ string, allocation treehouse.Allocation) error {
	f.releases = append(f.releases, allocation)
	return nil
}

func (*releaseCLI) Status(context.Context, string) ([]treehouse.WorktreeStatus, error) {
	return nil, errors.New("unexpected status")
}

type unusedGitInspector struct{}

func (unusedGitInspector) CreateTaskBranch(context.Context, string, string) (gitcontrol.Snapshot, error) {
	return gitcontrol.Snapshot{}, errors.New("unexpected task branch creation")
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
