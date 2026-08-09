package flow

import (
	"context"
	"testing"
	"time"

	"sophon/internal/domain"
	"sophon/internal/store"
	"sophon/internal/validation"
)

// publishReady drives a task to the derived ready state.
func publishReady(t *testing.T, home string, rig *testRig, taskID string) store.Spawn {
	t.Helper()
	ctx := context.Background()
	spawned, err := rig.flow.Spawn(ctx, taskID, false)
	if err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawned)
	if _, err := rig.flow.PublishResult(ctx, taskID, 1, testHeadSHA, writeResult(t, home, spawned, validResult)); err != nil {
		t.Fatal(err)
	}
	return spawned
}

// gitBranch points the single-branch fake Git at the spawn about to be
// verified, and marks its lease live.
func gitBranch(rig *testRig, spawn store.Spawn) {
	rig.git.mu.Lock()
	defer rig.git.mu.Unlock()
	rig.git.branch = spawn.Branch
	rig.leaseStatus(spawn)
}

func TestStatusDerivesActionQueue(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	ctx := context.Background()
	mission, err := rig.flow.CreateMission(ctx, "/repo", "Ship feature", "Implement and deliver the feature.")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rig.flow.CreateTask(ctx, mission.ID, "Plain task", "Implement the plain behavior.", "feature/plain", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	validated, err := rig.flow.CreateTask(ctx, mission.ID, "Validated task", "Implement validated behavior.", "feature/validated", "", "", "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := rig.flow.CreateTask(ctx, mission.ID, "Failing task", "Implement failing behavior.", "feature/failing", "", "", "go test ./...")
	if err != nil {
		t.Fatal(err)
	}

	// Every ready task queues its exact verify-complete command.
	spawnPlain := publishReady(t, home, rig, plain.ID)
	spawnValidated := publishReady(t, home, rig, validated.ID)
	spawnFailed := publishReady(t, home, rig, failed.ID)
	report, err := rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Action{
		{TaskID: plain.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + plain.ID},
		{TaskID: validated.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + validated.ID},
		{TaskID: failed.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + failed.ID},
	}
	assertActions(t, report, want)

	// Verifying a validated task replaces its verify action with a validate
	// action, ordered after every remaining verify action.
	gitBranch(rig, spawnValidated)
	if _, err := rig.flow.VerifyComplete(ctx, validated.ID); err != nil {
		t.Fatal(err)
	}
	report, err = rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertActions(t, report, []Action{
		{TaskID: plain.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + plain.ID},
		{TaskID: failed.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + failed.ID},
		{TaskID: validated.ID, Kind: ActionValidate, Command: "sophon validate " + validated.ID},
	})

	// A passing validation receipt drains the validate action.
	if _, err := rig.flow.Validate(ctx, validated.ID); err != nil {
		t.Fatal(err)
	}
	report, err = rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertActions(t, report, []Action{
		{TaskID: plain.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + plain.ID},
		{TaskID: failed.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + failed.ID},
	})

	// A failed validation receipt is terminal for the queue: re-running the
	// same failing command is not a deterministic action, correction routing
	// is commander judgment.
	gitBranch(rig, spawnFailed)
	if _, err := rig.flow.VerifyComplete(ctx, failed.ID); err != nil {
		t.Fatal(err)
	}
	rig.validate.result = validation.Result{Status: validation.Failed, ExitCode: 1}
	if record, err := rig.flow.Validate(ctx, failed.ID); err != nil || record.Passed {
		t.Fatalf("failed validation = %+v, %v", record, err)
	}
	report, err = rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertActions(t, report, []Action{
		{TaskID: plain.ID, Kind: ActionVerifyComplete, Command: "sophon verify-complete " + plain.ID},
	})

	// Verifying the last ready task drains the queue completely.
	gitBranch(rig, spawnPlain)
	if _, err := rig.flow.VerifyComplete(ctx, plain.ID); err != nil {
		t.Fatal(err)
	}
	report, err = rig.flow.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertActions(t, report, nil)
}

func TestForecasterUnstartedFixtureDerivesPlannedWithoutFakeWorker(t *testing.T) {
	useHome(t)
	rig := newRig()
	mission := store.Mission{ID: "mission_b6c051f852dde8b6d24b6a0f743d4ea1", ProjectPath: "/fixtures/forecaster",
		Title: "ERCOT forecast", Objective: "Build an ERCOT day-ahead forecasting baseline", CreatedAt: time.Now().UTC()}
	if err := store.CreateMission(mission); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "task_929c93ffa79b0ce6b19acaa5fe0c1039", MissionID: mission.ID,
		Title: "ERCOT day-ahead baseline", Objective: "Implement the forecasting baseline", DeliveryMode: domain.DeliveryLocal,
		Kind: domain.TaskImplementation, CreatedAt: time.Now().UTC()}
	if err := store.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceTask(mission.ID, task.ID, false); err != nil {
		t.Fatal(err)
	}
	report, err := rig.flow.Status(context.Background(), true)
	if err != nil || len(report.Missions) != 1 || len(report.Missions[0].Tasks) != 1 {
		t.Fatalf("status = %+v, %v", report, err)
	}
	status := report.Missions[0].Tasks[0]
	if status.State != store.StatePlanned || status.Attempt != 1 || len(report.Actions) != 1 || report.Actions[0].Kind != ActionStart {
		t.Fatalf("unstarted fixture = %+v actions=%+v", status, report.Actions)
	}
	if rig.panes.observeCalls != 0 {
		t.Fatalf("planned task invented a live worker observation: %d", rig.panes.observeCalls)
	}
}

func assertActions(t *testing.T, report Report, want []Action) {
	t.Helper()
	if len(report.Actions) != len(want) {
		t.Fatalf("actions = %+v, want %+v", report.Actions, want)
	}
	remaining := append([]Action(nil), report.Actions...)
	for _, action := range want {
		found := -1
		for i, candidate := range remaining {
			if candidate == action {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("actions = %+v, missing %+v", report.Actions, action)
		}
		remaining = append(remaining[:found], remaining[found+1:]...)
	}
	// Verify actions always precede validate actions.
	lastVerify, firstValidate := -1, len(report.Actions)
	for i, action := range report.Actions {
		if action.Kind == ActionVerifyComplete {
			lastVerify = i
		}
		if action.Kind == ActionValidate && i < firstValidate {
			firstValidate = i
		}
	}
	if lastVerify > firstValidate {
		t.Fatalf("validate action precedes a verify action: %+v", report.Actions)
	}
}
