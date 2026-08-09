package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sophon/internal/datahome"
	"sophon/internal/herdr"
	"sophon/internal/store"
)

// verifiedAttempt drives a task through spawn, publication, and verification
// so retirement tests start from successful terminal evidence.
func verifiedAttempt(t *testing.T, home string, rig *testRig, missionID, taskID string) store.Spawn {
	t.Helper()
	ctx := context.Background()
	spawned, err := rig.flow.Spawn(ctx, taskID, false)
	if err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawned)
	resultPath := writeResult(t, home, spawned, validResult)
	if _, err := rig.flow.PublishResult(ctx, taskID, 1, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.VerifyComplete(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	return spawned
}

func TestSpawnPropagatesResolvedDataHome(t *testing.T) {
	base := t.TempDir()
	// An override with a space and a redundant segment must reach the worker
	// as one clean absolute path.
	t.Setenv(datahome.OverrideEnv, filepath.Join(base, "smoke home", "..", "smoke home"))
	home := filepath.Join(base, "smoke home")
	rig := newRig()
	_, task := rig.createMissionAndTask(t, "", "")
	spawned, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	start := rig.panes.lastStart()
	if start.DataHome != home {
		t.Fatalf("StartRequest.DataHome = %q, want clean absolute %q", start.DataHome, home)
	}
	brief, err := os.ReadFile(store.AttemptPath(home, spawned.MissionID, task.ID, 1, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := datahome.OverrideEnv + "=" + shellQuote(home) + " sophon worker complete " + task.ID
	if !strings.Contains(string(brief), pinned) {
		t.Fatalf("brief completion command not pinned to the resolved data home:\n%s", brief)
	}
	if !strings.Contains(string(brief), "--result "+shellQuote(filepath.Join(store.AttemptDir(home, spawned.MissionID, task.ID, 1), store.CompletionSubmissionName))) {
		t.Fatalf("brief result path is not shell-quoted against spaces:\n%s", brief)
	}
	reportPinned := datahome.OverrideEnv + "=" + shellQuote(home) + " sophon worker report " + task.ID
	if !strings.Contains(string(brief), reportPinned) ||
		!strings.Contains(string(brief), "--report "+shellQuote(filepath.Join(store.AttemptDir(home, spawned.MissionID, task.ID, 1), store.ReportSubmissionName))) {
		t.Fatalf("brief report command/path not pinned and shell-quoted:\n%s", brief)
	}
}

func TestSpawnGroupsOnlyIntoSameSessionCommanderWorkspace(t *testing.T) {
	useHome(t)
	rig := newRig()
	rig.flow.deps.HerdrSession = "fm-lab-x"
	_, task := rig.createMissionAndTask(t, "", "")

	// No registration: isolated fallback.
	if _, err := rig.flow.Spawn(context.Background(), task.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := rig.panes.lastStart().ParentWorkspace; got != "" {
		t.Fatalf("ParentWorkspace without registration = %q, want empty", got)
	}

	// Registration in a different explicit session: still isolated.
	if err := store.PublishCommander(store.CommanderRegistration{Session: "other", WorkspaceID: "w9", PaneID: "w9:p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Spawn(context.Background(), task.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := rig.panes.lastStart().ParentWorkspace; got != "" {
		t.Fatalf("ParentWorkspace with foreign registration = %q, want empty", got)
	}

	// Registration in the same explicit session: group into that workspace.
	if err := store.PublishCommander(store.CommanderRegistration{Session: "fm-lab-x", WorkspaceID: "w9", TabID: "w9:t1", PaneID: "w9:p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Spawn(context.Background(), task.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := rig.panes.lastStart().ParentWorkspace; got != "w9" {
		t.Fatalf("ParentWorkspace with same-session registration = %q, want w9", got)
	}

	// A syntactically invalid registered workspace can never be targeted.
	if err := store.PublishCommander(store.CommanderRegistration{Session: "fm-lab-x", WorkspaceID: "w9;rm -rf /", TabID: "w9:t1", PaneID: "w9:p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Spawn(context.Background(), task.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := rig.panes.lastStart().ParentWorkspace; got != "" {
		t.Fatalf("ParentWorkspace with malformed workspace = %q, want empty", got)
	}
}

func TestAttachCommanderValidatesAndRegisters(t *testing.T) {
	useHome(t)
	fake := &fakeSessionPanes{runtime: herdr.RuntimeClaude}
	var requested []string
	rig := newRig()
	rig.flow.deps.NewSessionPanes = sessionPanesFactory(fake, &requested)

	registration, err := rig.flow.AttachCommander(context.Background(), AttachRequest{
		Session: "fm-lab-x", WorkspaceID: "w9", TabID: "w9:t1", PaneID: "w9:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if registration.Runtime != string(herdr.RuntimeClaude) || registration.Session != "fm-lab-x" ||
		registration.WorkspaceID != "w9" || registration.TabID != "w9:t1" || registration.PaneID != "w9:p1" {
		t.Fatalf("registration = %+v", registration)
	}
	if len(requested) != 1 || requested[0] != "fm-lab-x" {
		t.Fatalf("pane factory sessions = %v", requested)
	}
	loaded, err := store.ReadCommander()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != registration {
		t.Fatalf("persisted registration = %+v, want %+v", loaded, registration)
	}

	for _, test := range []struct {
		name string
		in   AttachRequest
	}{
		{"bad session", AttachRequest{Session: "no spaces", PaneID: "w9:p1"}},
		{"empty pane", AttachRequest{Session: "fm-lab-x"}},
		{"bad pane", AttachRequest{Session: "fm-lab-x", PaneID: "w9:p1;touch /tmp/x"}},
		{"lone workspace", AttachRequest{Session: "fm-lab-x", WorkspaceID: "w9", PaneID: "w9:p1"}},
		{"bad workspace", AttachRequest{Session: "fm-lab-x", WorkspaceID: "../w9", TabID: "w9:t1", PaneID: "w9:p1"}},
	} {
		if _, err := rig.flow.AttachCommander(context.Background(), test.in); err == nil {
			t.Errorf("%s: attach accepted %+v", test.name, test.in)
		}
	}
	// A husk or lost pane is not a live commander and must be refused.
	for _, state := range []herdr.State{herdr.StateHusk, herdr.StateLost} {
		fake.state = state
		if _, err := rig.flow.AttachCommander(context.Background(), AttachRequest{Session: "fm-lab-x", PaneID: "w9:p1"}); err == nil {
			t.Errorf("attach accepted %s pane", state)
		}
	}
}

func TestNotifyCommanderIsBestEffort(t *testing.T) {
	home := useHome(t)
	fake := &fakeSessionPanes{runtime: herdr.RuntimeCodex}
	var requested []string
	rig := newRig()
	rig.flow.deps.NewSessionPanes = sessionPanesFactory(fake, &requested)
	ctx := context.Background()

	// No registration: a quiet no-op.
	if err := rig.flow.NotifyCommander(ctx, "task_x", 1); err != nil {
		t.Fatalf("notify without registration = %v", err)
	}
	if err := store.PublishCommander(store.CommanderRegistration{Session: "fm-lab-x", PaneID: "w9:p1", Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.NotifyCommander(ctx, "task_x", 2); err != nil {
		t.Fatal(err)
	}
	if len(fake.submissions) != 1 || !strings.HasPrefix(fake.submissions[0], "w9:p1: ") ||
		!strings.Contains(fake.submissions[0], "task_x") || !strings.Contains(fake.submissions[0], "ready") ||
		!strings.Contains(fake.submissions[0], "sophon status") ||
		!strings.Contains(fake.submissions[0], "sophon verify-complete task_x") {
		t.Fatalf("commander wake = %v", fake.submissions)
	}
	if strings.Contains(fake.submissions[0], "sophon validate") {
		t.Fatalf("wake for an unknown task must not promise validation: %v", fake.submissions[0])
	}
	if len(requested) != 1 || requested[0] != "fm-lab-x" {
		t.Fatalf("notify routed to sessions %v, want [fm-lab-x]", requested)
	}

	// A task with a configured validation command gets the exact validate
	// command in its wake.
	_, validated := rig.createMissionAndTask(t, "", "go test ./...")
	if _, err := store.BumpAttempt(validated.MissionID, validated.ID); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.NotifyCommander(ctx, validated.ID, 1); err != nil {
		t.Fatal(err)
	}
	if len(fake.submissions) != 2 ||
		!strings.Contains(fake.submissions[1], "sophon verify-complete "+validated.ID) ||
		!strings.Contains(fake.submissions[1], "sophon validate "+validated.ID) {
		t.Fatalf("validated task wake = %v", fake.submissions)
	}

	// Correction publications name their distinct derived state while using
	// the same action-drain path as initial work.
	if got := CommanderCorrectionWakeMessage(validated.ID, 2, true); !strings.Contains(got, "derives correction-ready") ||
		!strings.Contains(got, "sophon verify-complete "+validated.ID) ||
		!strings.Contains(got, "sophon validate "+validated.ID) {
		t.Fatalf("correction wake = %q", got)
	}

	// Delivery failure surfaces as a bounded diagnostic error.
	fake.submitErr = errFake
	if err := rig.flow.NotifyCommander(ctx, "task_x", 2); !errors.Is(err, errFake) {
		t.Fatalf("notify with dead target = %v, want wrapped %v", err, errFake)
	}

	// A malformed record is a diagnostic, never a panic or a state change.
	if err := os.WriteFile(store.CommanderPath(home), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.NotifyCommander(ctx, "task_x", 2); err == nil {
		t.Fatal("notify with malformed registration must surface a diagnostic")
	}
}

func TestRetireWorkerBoundaries(t *testing.T) {
	home := useHome(t)
	fake := &fakeSessionPanes{}
	var requested []string
	rig := newRig()
	rig.panes.session = herdr.Session{SessionName: "fm-lab-x", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1"}
	rig.flow.deps.NewSessionPanes = sessionPanesFactory(fake, &requested)
	ctx := context.Background()

	// No outcome yet: not terminal, nothing happens.
	_, unverified := rig.createMissionAndTask(t, "", "")
	if _, err := rig.flow.Spawn(ctx, unverified.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.RetireWorker(ctx, unverified.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.stops) != 0 {
		t.Fatalf("retired an unverified worker: %v", fake.stops)
	}

	// Verified, no validation: the exact recorded tab closes.
	_, plain := rig.createMissionAndTask(t, "", "")
	spawned := verifiedAttempt(t, home, rig, plain.MissionID, plain.ID)
	if err := rig.flow.RetireWorker(ctx, plain.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.stops) != 1 || fake.stops[0].TabID != spawned.Pane.TabID || fake.stops[0].SessionName != "fm-lab-x" {
		t.Fatalf("retirement stops = %+v, want exact tab %s in fm-lab-x", fake.stops, spawned.Pane.TabID)
	}
	if len(requested) != 1 || requested[0] != "fm-lab-x" {
		t.Fatalf("retirement routed to sessions %v", requested)
	}

	// Idempotent: the exact pane now observes lost; a repeat is quiet success.
	fake.state = herdr.StateLost
	if err := rig.flow.RetireWorker(ctx, plain.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.stops) != 1 {
		t.Fatalf("lost pane was stopped again: %v", fake.stops)
	}
	fake.state = ""

	// Verified with validation configured but not yet passed: stays available.
	_, gated := rig.createMissionAndTask(t, "", "exit 0")
	gatedSpawn := verifiedAttempt(t, home, rig, gated.MissionID, gated.ID)
	if err := rig.flow.RetireWorker(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.stops) != 1 {
		t.Fatalf("validated task retired before validation: %v", fake.stops)
	}

	// A failed validation keeps the correction path open.
	rig.validate.result.Status = "failed"
	rig.validate.result.ExitCode = 1
	if _, err := rig.flow.Validate(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.RetireWorker(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.stops) != 1 {
		t.Fatalf("failed validation retired the worker: %v", fake.stops)
	}

	// A passing validation on a replacement attempt is terminal: the exact tab
	// closes, while the failed receipt above remains immutable history.
	replacementSpawn, err := rig.flow.Spawn(ctx, gated.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	gatedSpawn = replacementSpawn
	resultPath := writeResult(t, home, gatedSpawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, gated.ID, gatedSpawn.Attempt, testHeadSHA, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(gatedSpawn)
	if _, err := rig.flow.VerifyComplete(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	rig.validate.result.Status = "passed"
	rig.validate.result.ExitCode = 0
	if _, err := rig.flow.Validate(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.RetireWorker(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.stops) != 2 || fake.stops[1].TabID != gatedSpawn.Pane.TabID {
		t.Fatalf("post-validation retirement stops = %+v", fake.stops)
	}

	// A close failure is a bounded diagnostic; the outcome stays intact.
	fake.stopErr = errFake
	if err := rig.flow.RetireWorker(ctx, gated.ID); !errors.Is(err, errFake) {
		t.Fatalf("retirement with failing close = %v", err)
	}
	if _, err := store.ReadOutcome(gated.MissionID, gated.ID, 2); err != nil {
		t.Fatalf("outcome was affected by cleanup failure: %v", err)
	}
	fake.stopErr = nil

	// Invalid recorded identity refuses rather than touching another pane.
	spawned.Pane.TabID = "w1:t1;rm"
	if err := store.Publish(store.AttemptPath(home, plain.MissionID, plain.ID, 1, "spawn.json"), spawned); err != nil {
		t.Fatal(err)
	}
	if err := rig.flow.RetireWorker(ctx, plain.ID); err == nil {
		t.Fatal("retirement accepted malformed recorded identity")
	}
	if len(fake.stops) != 2 {
		t.Fatalf("malformed identity closed another pane: %v", fake.stops)
	}
}
