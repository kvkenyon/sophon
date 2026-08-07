package validation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sophon/internal/domain"
)

func TestPipelineCacheKeyDimensions(t *testing.T) {
	tests := []struct {
		name   string
		change func(*pipelineFixture)
	}{
		{"task", func(f *pipelineFixture) {
			f.request.TaskID = "tsk_other"
			f.store.tasks["tsk_other"] = readyTask("tsk_other")
			f.store.attempts["tsk_other"] = completedAttempt("tsk_other", f.workspace.value.HeadSHA)
		}},
		{"head SHA", func(f *pipelineFixture) {
			f.workspace.value.HeadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			attempt := f.store.attempts[f.request.TaskID]
			attempt.HeadSHA = f.workspace.value.HeadSHA
			f.store.attempts[f.request.TaskID] = attempt
		}},
		{"dirty tree fingerprint", func(f *pipelineFixture) { f.workspace.value.DirtyTreeHash = "dirty-b" }},
		{"validator kind", func(f *pipelineFixture) { f.validator.kind = Lint }},
		{"validator version", func(f *pipelineFixture) { f.validator.version = "v2" }},
		{"validation config", func(f *pipelineFixture) { f.request.Config = []byte("config-b") }},
		{"command", func(f *pipelineFixture) { f.validator.command = []string{"fake", "changed"} }},
		{"environment", func(f *pipelineFixture) { f.environment.value = "environment-b" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(Passed)
			first, err := fixture.pipeline.ValidateTask(context.Background(), fixture.request)
			if err != nil || !first.Passed || first.CacheHits != 0 || fixture.validator.runs != 1 {
				t.Fatalf("first run = %+v, runs=%d, err=%v", first, fixture.validator.runs, err)
			}
			fixture.request.CommandID = "cmd_same_inputs"
			cached, err := fixture.pipeline.ValidateTask(context.Background(), fixture.request)
			if err != nil || !cached.Passed || cached.CacheHits != 1 || fixture.validator.runs != 1 || !cached.Runs[0].Cached {
				t.Fatalf("cached run = %+v, runs=%d, err=%v", cached, fixture.validator.runs, err)
			}

			test.change(fixture)
			fixture.request.CommandID = "cmd_changed_input"
			miss, err := fixture.pipeline.ValidateTask(context.Background(), fixture.request)
			if err != nil || !miss.Passed || miss.CacheHits != 0 || fixture.validator.runs != 2 || miss.Runs[0].Cached {
				t.Fatalf("changed input run = %+v, runs=%d, err=%v", miss, fixture.validator.runs, err)
			}
		})
	}
}

func TestPipelineCachesFailedResultAsFailure(t *testing.T) {
	fixture := newPipelineFixture(Failed)
	first, err := fixture.pipeline.ValidateTask(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Passed || first.Task.State != domain.TaskDeliveryBlocked || fixture.validator.runs != 1 {
		t.Fatalf("first failed run = %+v, runs=%d", first, fixture.validator.runs)
	}
	fixture.request.CommandID = "cmd_retry_failure"
	second, err := fixture.pipeline.ValidateTask(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Passed || second.Task.State != domain.TaskDeliveryBlocked || second.CacheHits != 1 ||
		fixture.validator.runs != 1 || second.Runs[0].Record.Status != Failed {
		t.Fatalf("cached failure = %+v, runs=%d", second, fixture.validator.runs)
	}
}

func TestGitFingerprinterIncludesDirtyTreeContent(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init")
	runGit(t, worktree, "config", "user.name", "Validation Test")
	runGit(t, worktree, "config", "user.email", "validation@example.invalid")
	writeTestFile(t, filepath.Join(worktree, "tracked.txt"), "clean\n")
	runGit(t, worktree, "add", "tracked.txt")
	runGit(t, worktree, "commit", "-m", "initial")

	fingerprinter := GitFingerprinter{}
	clean, err := fingerprinter.Fingerprint(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(worktree, "tracked.txt"), "dirty\n")
	dirty, err := fingerprinter.Fingerprint(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if clean.HeadSHA != dirty.HeadSHA || clean.DirtyTreeHash == dirty.DirtyTreeHash {
		t.Fatalf("clean=%+v dirty=%+v", clean, dirty)
	}
	writeTestFile(t, filepath.Join(worktree, "untracked.txt"), "one\n")
	untrackedOne, err := fingerprinter.Fingerprint(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(worktree, "untracked.txt"), "two\n")
	untrackedTwo, err := fingerprinter.Fingerprint(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if untrackedOne.DirtyTreeHash == untrackedTwo.DirtyTreeHash {
		t.Fatal("untracked file content did not affect dirty-tree fingerprint")
	}
}

type pipelineFixture struct {
	store       *memoryStore
	workspace   *fakeWorkspace
	environment *fakeEnvironment
	validator   *countingValidator
	pipeline    Pipeline
	request     Request
}

func newPipelineFixture(status Status) *pipelineFixture {
	taskID := domain.TaskID("tsk_one")
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &memoryStore{
		tasks:    map[domain.TaskID]domain.Task{taskID: readyTask(taskID)},
		attempts: map[domain.TaskID]domain.TaskAttempt{taskID: completedAttempt(taskID, head)},
		cache:    make(map[string]Record), records: make(map[string]Record),
	}
	workspace := &fakeWorkspace{value: Workspace{HeadSHA: head, DirtyTreeHash: "dirty-a"}}
	environment := &fakeEnvironment{value: "environment-a"}
	validator := &countingValidator{kind: UnitTests, version: "v1", command: []string{"fake", "test"}, status: status}
	fixture := &pipelineFixture{store: store, workspace: workspace, environment: environment, validator: validator}
	fixture.pipeline = Pipeline{Store: store, Workspace: workspace, Environment: environment}
	fixture.request = Request{
		TaskID: taskID, CommandID: "cmd_initial", Validators: []Validator{validator}, Config: []byte("config-a"),
	}
	return fixture
}

func readyTask(id domain.TaskID) domain.Task {
	return domain.Task{ID: id, MissionID: "msn_one", State: domain.TaskReady, Version: 7,
		DeliveryMode: domain.DeliveryGate, CurrentAttempt: 1}
}

func completedAttempt(id domain.TaskID, head string) domain.TaskAttempt {
	return domain.TaskAttempt{TaskID: id, Attempt: 1, HeadSHA: head, WorktreePath: "/fake/worktree"}
}

type countingValidator struct {
	kind    Kind
	version string
	command []string
	status  Status
	runs    int
}

func (v *countingValidator) Kind() Kind      { return v.kind }
func (v *countingValidator) Version() string { return v.version }
func (v *countingValidator) Command() []string {
	return append([]string(nil), v.command...)
}
func (v *countingValidator) Run(context.Context, string) (Result, error) {
	v.runs++
	exitCode := 0
	if v.status == Failed {
		exitCode = 1
	}
	return Result{Status: v.status, ExitCode: exitCode, Output: "evidence",
		StartedAt: time.Unix(1, 0).UTC(), Duration: time.Second}, nil
}

type fakeWorkspace struct{ value Workspace }

func (f *fakeWorkspace) Fingerprint(context.Context, string) (Workspace, error) { return f.value, nil }

type fakeEnvironment struct{ value string }

func (f *fakeEnvironment) Fingerprint() (string, error) { return f.value, nil }

type memoryStore struct {
	tasks    map[domain.TaskID]domain.Task
	attempts map[domain.TaskID]domain.TaskAttempt
	cache    map[string]Record
	records  map[string]Record
	nextID   int
}

func (s *memoryStore) Task(_ context.Context, id domain.TaskID) (domain.Task, error) {
	task, ok := s.tasks[id]
	if !ok {
		return domain.Task{}, fmt.Errorf("missing task")
	}
	return task, nil
}
func (s *memoryStore) Attempt(_ context.Context, id domain.TaskID, _ int) (domain.TaskAttempt, error) {
	return s.attempts[id], nil
}
func (s *memoryStore) BeginValidation(_ context.Context, _ domain.CommandID, in BeginInput) (domain.Task, error) {
	task := s.tasks[in.TaskID]
	if task.State == domain.TaskReady || task.State == domain.TaskDeliveryBlocked {
		task.State = domain.TaskValidating
		task.Version++
	}
	s.tasks[in.TaskID] = task
	return task, nil
}
func (s *memoryStore) LookupValidation(_ context.Context, key Key) (*Record, error) {
	record, ok := s.cache[key.Digest()]
	if !ok {
		return nil, nil
	}
	return &record, nil
}
func (s *memoryStore) RecordValidation(_ context.Context, _ domain.CommandID, in RecordInput) (Record, error) {
	s.nextID++
	id := fmt.Sprintf("val_%d", s.nextID)
	evidence, _ := hashJSON(in.Result)
	record := Record{ID: id, Attempt: in.Attempt, Key: in.Key, Status: in.Result.Status, Result: in.Result,
		Artifact: Artifact{ID: domain.ArtifactID("art_" + id), TaskID: in.TaskID, Attempt: in.Attempt,
			SHA256: evidence, Content: []byte(in.Result.Output)}, CreatedAt: time.Unix(2, 0).UTC()}
	s.cache[in.Key.Digest()] = record
	s.records[id] = record
	return record, nil
}
func (s *memoryStore) CompleteValidation(_ context.Context, _ domain.CommandID, in CompleteInput) (domain.Task, error) {
	task := s.tasks[in.TaskID]
	for _, id := range in.RunIDs {
		if s.records[id].Status == Failed {
			task.State = domain.TaskDeliveryBlocked
			task.Version++
		}
	}
	s.tasks[in.TaskID] = task
	return task, nil
}

func runGit(t *testing.T, path string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", path}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
