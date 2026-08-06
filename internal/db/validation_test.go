package db

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/validation"
)

func TestValidationRunPersistsArtifactAndPassedEvents(t *testing.T) {
	store, task := readyValidationTask(t)
	defer store.Close()
	ctx := context.Background()

	validating, err := store.BeginValidation(ctx, "cmd_validation_begin", validation.BeginInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := validationTestKey(task.ID)
	recorded, err := store.RecordValidation(ctx, "cmd_validation_record", validation.RecordInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, Key: key,
		Result: validation.Result{Status: validation.Passed, ExitCode: 0, Output: "all tests passed",
			StartedAt: time.Unix(10, 0).UTC(), Duration: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := store.LookupValidation(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.ID != recorded.ID || cached.Artifact.ID != recorded.Artifact.ID {
		t.Fatalf("cached record = %+v, recorded = %+v", cached, recorded)
	}
	var evidence validation.Result
	if err := json.Unmarshal(cached.Artifact.Content, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Output != "all tests passed" || cached.Artifact.SHA256 == "" {
		t.Fatalf("artifact evidence = %+v, artifact = %+v", evidence, cached.Artifact)
	}

	completed, err := store.CompleteValidation(ctx, "cmd_validation_complete", validation.CompleteInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedVersion: validating.Version,
		HeadSHA: key.HeadSHA, WorkspaceHash: key.WorkspaceHash, ConfigHash: key.ConfigHash,
		EnvironmentHash: key.EnvironmentHash, RunIDs: []string{recorded.ID}, Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.TaskValidating || completed.Version != validating.Version {
		t.Fatalf("passed validation task = %+v", completed)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEventTypes(t, events, "task.validating", "validation.started", "validation.passed")

	var runCount, artifactCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM validation_runs WHERE id = ?", recorded.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM artifacts WHERE id = ?", recorded.Artifact.ID).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || artifactCount != 1 {
		t.Fatalf("runs=%d artifacts=%d", runCount, artifactCount)
	}
}

func TestFailedValidationIsCachedAsFailedAndBlocksDelivery(t *testing.T) {
	store, task := readyValidationTask(t)
	defer store.Close()
	ctx := context.Background()
	validating, err := store.BeginValidation(ctx, "cmd_failed_begin", validation.BeginInput{
		TaskID: task.ID, Attempt: 1, Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := validationTestKey(task.ID)
	recorded, err := store.RecordValidation(ctx, "cmd_failed_record", validation.RecordInput{
		TaskID: task.ID, Attempt: 1, Key: key,
		Result: validation.Result{Status: validation.Failed, ExitCode: 2, Output: "test failure",
			StartedAt: time.Unix(20, 0).UTC(), Duration: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := store.CompleteValidation(ctx, "cmd_failed_complete", validation.CompleteInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: validating.Version,
		HeadSHA: key.HeadSHA, WorkspaceHash: key.WorkspaceHash, ConfigHash: key.ConfigHash,
		EnvironmentHash: key.EnvironmentHash, RunIDs: []string{recorded.ID}, Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != domain.TaskDeliveryBlocked || blocked.Version != validating.Version+1 {
		t.Fatalf("failed validation task = %+v", blocked)
	}
	cached, err := store.LookupValidation(ctx, key)
	if err != nil || cached == nil || cached.Status != validation.Failed {
		t.Fatalf("cached failure = %+v, err=%v", cached, err)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEventTypes(t, events, "validation.failed", "task.delivery_blocked")
}

func TestValidationRecordCommandIsIdempotent(t *testing.T) {
	store, task := readyValidationTask(t)
	defer store.Close()
	if _, err := store.BeginValidation(context.Background(), "cmd_idempotent_begin", validation.BeginInput{
		TaskID: task.ID, Attempt: 1, Actor: "commander",
	}); err != nil {
		t.Fatal(err)
	}
	in := validation.RecordInput{
		TaskID: task.ID, Attempt: 1, Key: validationTestKey(task.ID),
		Result: validation.Result{Status: validation.Passed, ExitCode: 0, Output: "same",
			StartedAt: time.Unix(30, 0).UTC(), Duration: time.Second},
	}
	first, err := store.RecordValidation(context.Background(), "cmd_record_same", in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RecordValidation(context.Background(), "cmd_record_same", in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Artifact.ID != second.Artifact.ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func readyValidationTask(t *testing.T) (*Store, domain.Task) {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	task := createTestTask(t, store, domain.TaskImplementation, domain.DeliveryGate, 3)
	var err error
	task, err = store.TransitionTask(ctx, "cmd_validation_provision", TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
		To: domain.TaskProvisioning, Actor: "test",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	_, err = store.RecordTreehouseLease(ctx, "cmd_validation_lease", RecordTreehouseLeaseInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, Actor: "test",
		Lease: domain.TreehouseLease{LeaseID: "lease-validation", LeaseHolder: "holder-validation",
			WorktreePath: "/validation/worktree", Project: "project", Branch: "validation-branch", BaseSHA: "base-sha"},
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
		task, err = store.TransitionTask(ctx, domain.CommandID("cmd_validation_"+string(state)), TransitionTaskInput{
			TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
			To: state, Actor: "test",
		})
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	task, err = store.CompleteWorkerTask(ctx, "cmd_validation_worker_complete", CompleteWorkerTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version,
		LeaseID: "lease-validation", LeaseHolder: "holder-validation", HeadSHA: "head-sha",
		ResultPath: "/result.json", ResultSHA256: "result-hash", Actor: "worker",
		Result: domain.WorkerResult{Version: 1, Status: "completed", Summary: "done",
			Verification: []domain.VerificationResult{{Command: "fake", ExitCode: 0}},
			ChangedFiles: []string{"file.go"}, Risks: []string{}},
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, task
}

func validationTestKey(taskID domain.TaskID) validation.Key {
	return validation.Key{
		TaskID: taskID, HeadSHA: "head-sha", WorkspaceHash: "workspace-hash",
		Validator: validation.UnitTests, ValidatorVersion: "v1", ConfigHash: "config-hash",
		CommandHash: "command-hash", EnvironmentHash: "environment-hash",
	}
}

func assertEventTypes(t *testing.T, events []domain.Event, expected ...string) {
	t.Helper()
	found := make(map[string]bool, len(events))
	for _, event := range events {
		found[event.Type] = true
	}
	for _, eventType := range expected {
		if !found[eventType] {
			t.Fatalf("missing event %q in %+v", eventType, events)
		}
	}
}
