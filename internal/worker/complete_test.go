package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/treehouse"
)

type completionFixture struct {
	store      *db.Store
	task       domain.Task
	attempt    domain.TaskAttempt
	lease      domain.TreehouseLease
	repo       string
	head       string
	files      BriefGenerator
	resultPath string
	observer   *fakeLeaseObserver
}

type fakeLeaseObserver struct {
	statuses []treehouse.WorktreeStatus
	err      error
}

func (f *fakeLeaseObserver) Status(context.Context, string) ([]treehouse.WorktreeStatus, error) {
	return f.statuses, f.err
}

func TestCompleterAcceptsVerifiedOutcomeAndAtomicallyReachesReady(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	completed, err := fixture.completer().Complete(context.Background(), CompleteRequest{
		TaskID: fixture.task.ID, Attempt: 1, HeadSHA: fixture.head,
		ResultPath: fixture.resultPath, CommandID: "cmd_complete_valid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.TaskReady || completed.Version != fixture.task.Version+2 {
		t.Fatalf("completed task = %+v", completed)
	}
	attempt, err := fixture.store.Attempt(context.Background(), fixture.task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.HeadSHA != fixture.head || attempt.CompletedAt == nil {
		t.Fatalf("completed attempt = %+v", attempt)
	}
	events, err := fixture.store.TaskEvents(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var collecting, ready bool
	for _, event := range events {
		collecting = collecting || event.Type == "task.collecting"
		ready = ready || event.Type == "task.ready"
	}
	if !collecting || !ready {
		t.Fatalf("completion events = %+v", events)
	}
	replayed, err := fixture.completer().Complete(context.Background(), CompleteRequest{
		TaskID: fixture.task.ID, Attempt: 1, HeadSHA: fixture.head,
		ResultPath: fixture.resultPath, CommandID: "cmd_complete_valid",
	})
	if err != nil || replayed.State != domain.TaskReady || replayed.Version != completed.Version {
		t.Fatalf("idempotent completion replay = %+v, %v", replayed, err)
	}
	afterReplay, err := fixture.store.TaskEvents(context.Background(), fixture.task.ID)
	if err != nil || len(afterReplay) != len(events) {
		t.Fatalf("completion replay emitted events: before=%d after=%d err=%v", len(events), len(afterReplay), err)
	}
}

func TestCompletionResumerVerifiesExternalStateAndConvergesOnOneCommand(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	resumer := CompletionResumer{Store: fixture.store, Completer: fixture.completer(), Git: gitcontrol.NewClient()}
	first, err := resumer.Resume(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resumer.Resume(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != domain.TaskReady || second.State != domain.TaskReady || first.Version != second.Version {
		t.Fatalf("completion recovery did not converge: first=%+v second=%+v", first, second)
	}
	events, err := fixture.store.TaskEvents(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	readyEvents := 0
	for _, event := range events {
		if event.Type == "task.ready" {
			readyEvents++
		}
	}
	if readyEvents != 1 {
		t.Fatalf("completion recovery emitted %d ready events", readyEvents)
	}
}

func TestCompleterFencesWrongAttempt(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	_, err := fixture.completer().Complete(context.Background(), CompleteRequest{
		TaskID: fixture.task.ID, Attempt: 2, HeadSHA: fixture.head,
		ResultPath: fixture.resultPath, CommandID: "cmd_wrong_attempt",
	})
	if !errors.Is(err, db.ErrStaleAttempt) {
		t.Fatalf("wrong-attempt error = %v, want ErrStaleAttempt", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsInvalidLeaseIdentity(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	fixture.observer.statuses[0].LeaseID = "replacement-lease"
	_, err := fixture.completer().Complete(context.Background(), fixture.request("cmd_bad_lease"))
	if !errors.Is(err, db.ErrLeaseConflict) {
		t.Fatalf("lease error = %v, want ErrLeaseConflict", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsReportedHeadMismatch(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	request := fixture.request("cmd_bad_sha")
	request.HeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := fixture.completer().Complete(context.Background(), request)
	if !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("bad-head error = %v, want ErrHeadMismatch", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsDirtyTree(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	writeTestFile(t, filepath.Join(fixture.repo, "dirty.txt"), "uncommitted\n")
	_, err := fixture.completer().Complete(context.Background(), fixture.request("cmd_dirty"))
	if !errors.Is(err, gitcontrol.ErrDirtyTree) {
		t.Fatalf("dirty-tree error = %v, want ErrDirtyTree", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsMissingCommit(t *testing.T) {
	fixture := newCompletionFixture(t, false)
	_, err := fixture.completer().Complete(context.Background(), fixture.request("cmd_no_commit"))
	if !errors.Is(err, gitcontrol.ErrNoNewCommit) {
		t.Fatalf("missing-commit error = %v, want ErrNoNewCommit", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsNonDescendantHead(t *testing.T) {
	fixture := newCompletionFixture(t, false)
	runTestGit(t, fixture.repo, "checkout", "--orphan", "unrelated")
	runTestGit(t, fixture.repo, "rm", "-f", "base.txt")
	writeTestFile(t, filepath.Join(fixture.repo, "other.txt"), "unrelated\n")
	runTestGit(t, fixture.repo, "add", "other.txt")
	runTestGit(t, fixture.repo, "commit", "-m", "unrelated")
	fixture.head = runTestGit(t, fixture.repo, "rev-parse", "HEAD")
	_, err := fixture.completer().Complete(context.Background(), fixture.request("cmd_unrelated"))
	if !errors.Is(err, gitcontrol.ErrNotDescendant) {
		t.Fatalf("non-descendant error = %v, want ErrNotDescendant", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsStaleResult(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	old := fixture.attempt.StartedAt.Add(-time.Hour)
	if err := os.Chtimes(fixture.resultPath, old, old); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.completer().Complete(context.Background(), fixture.request("cmd_stale_result"))
	if !errors.Is(err, ErrStaleResult) {
		t.Fatalf("stale-result error = %v, want ErrStaleResult", err)
	}
	assertTaskStillRunning(t, fixture)
}

func TestCompleterRejectsMalformedResultSchema(t *testing.T) {
	fixture := newCompletionFixture(t, true)
	writeTestFile(t, fixture.resultPath, `{"version":1,"status":"completed","summary":"ok","verification":[],"changed_files":["change.txt"],"risks":[],"extra":true}`)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(fixture.resultPath, future, future); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.completer().Complete(context.Background(), fixture.request("cmd_bad_result"))
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("invalid-result error = %v, want ErrInvalidResult", err)
	}
	assertTaskStillRunning(t, fixture)
}

func (f completionFixture) request(command domain.CommandID) CompleteRequest {
	return CompleteRequest{TaskID: f.task.ID, Attempt: 1, HeadSHA: f.head, ResultPath: f.resultPath, CommandID: command}
}

func (f completionFixture) completer() *Completer {
	return &Completer{Store: f.store, Git: gitcontrol.NewClient(), Leases: f.observer, TaskFiles: f.files}
}

func newCompletionFixture(t *testing.T, newCommit bool) completionFixture {
	t.Helper()
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init", "-b", "task-branch")
	runTestGit(t, repo, "config", "user.name", "Parallel Intellect Test")
	runTestGit(t, repo, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(repo, "base.txt"), "base\n")
	runTestGit(t, repo, "add", "base.txt")
	runTestGit(t, repo, "commit", "-m", "base")
	base := runTestGit(t, repo, "rev-parse", "HEAD")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	suffix := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	projectID, err := store.CreateProject(ctx, domain.CommandID("cmd_project_"+suffix), db.CreateProjectInput{Name: "project-" + suffix, Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, domain.CommandID("cmd_mission_"+suffix), db.CreateMissionInput{
		ProjectID: projectID, Title: "mission", Objective: "objective", Budget: domain.MissionBudget{MaxTaskAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, domain.CommandID("cmd_task_"+suffix), db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "task", Objective: "objective",
		AcceptanceCriteria: []domain.Criterion{{Description: "works"}}, WorkerAgent: "codex", DeliveryMode: domain.DeliveryBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	task = transitionFixture(t, store, task, domain.TaskProvisioning, "provision")
	lease, err := store.RecordTreehouseLease(ctx, domain.CommandID("cmd_lease_"+suffix), db.RecordTreehouseLeaseInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, Actor: "test",
		Lease: domain.TreehouseLease{LeaseID: "lease-" + suffix, LeaseHolder: treehouse.LeaseHolder(task.ID, 1),
			WorktreePath: repo, Project: "project-" + suffix, Branch: "task-branch", BaseSHA: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = transitionFixture(t, store, task, domain.TaskStarting, "starting")
	_, err = store.RecordWorkerSession(ctx, domain.CommandID("cmd_worker_"+suffix), db.RecordWorkerSessionInput{
		TaskID: task.ID, Attempt: 1, Actor: "test",
		Session: domain.WorkerSession{ID: domain.SessionID("wsn_" + suffix), Runtime: "codex", HerdrSessionName: "fm-lab-test",
			HerdrWorkspaceID: "w1", HerdrTabID: "w1:t1", HerdrPaneID: "w1:p1",
			HerdrAgentName: "pi-task-a1", AgentSessionID: "codex-session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.Attempt(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if newCommit {
		writeTestFile(t, filepath.Join(repo, "change.txt"), "change\n")
		runTestGit(t, repo, "add", "change.txt")
		runTestGit(t, repo, "commit", "-m", "change")
	}
	head := runTestGit(t, repo, "rev-parse", "HEAD")
	files := BriefGenerator{BaseDir: filepath.Join(t.TempDir(), "task-files")}
	attemptDir, err := files.AttemptDir(task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(attemptDir, "result.json")
	writeTestFile(t, resultPath, `{"version":1,"status":"completed","summary":"implemented","verification":[{"command":"go test ./...","exit_code":0}],"changed_files":["change.txt"],"risks":[]}`)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(resultPath, future, future); err != nil {
		t.Fatal(err)
	}
	observer := &fakeLeaseObserver{statuses: []treehouse.WorktreeStatus{{WorktreePath: repo, Status: "leased", LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder}}}
	return completionFixture{store: store, task: task, attempt: attempt, lease: lease, repo: repo, head: head,
		files: files, resultPath: resultPath, observer: observer}
}

func transitionFixture(t *testing.T, store *db.Store, task domain.Task, to domain.TaskState, suffix string) domain.Task {
	t.Helper()
	updated, err := store.TransitionTask(context.Background(), domain.CommandID(fmt.Sprintf("cmd_%s_%s", suffix, t.Name())), db.TransitionTaskInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State, ExpectedVersion: task.Version, To: to, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func assertTaskStillRunning(t *testing.T, fixture completionFixture) {
	t.Helper()
	current, err := fixture.store.Task(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.TaskRunning || current.Version != fixture.task.Version {
		t.Fatalf("failed completion mutated task: %+v", current)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
