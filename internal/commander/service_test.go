package commander

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
	signalpolicy "parallel-intellect/internal/signals"
)

type fakeCommanderRuntime struct {
	state       State
	startConfig StartConfig
	starts      int
	prompts     []string
	steers      []string
	followUps   []string
	aborts      int
	replacement *Session
	resumeErr   error
}

func (f *fakeCommanderRuntime) Start(_ context.Context, config StartConfig) (Session, error) {
	f.starts++
	f.startConfig = config
	tab, pane := "w1:t1", "w1:p1"
	if f.starts > 1 {
		tab, pane = "w1:t3", "w1:p3"
	}
	return Session{ID: config.SessionID, MissionID: config.MissionID, Runtime: config.Runtime, Herdr: herdr.Session{
		Runtime: config.Runtime, AgentName: "pi-commander-" + string(config.MissionID),
		AgentSessionID: "agent-session-1", SessionName: "fm-lab-commanders", WorkspaceID: "w1",
		TabID: tab, PaneID: pane, WorktreePath: config.WorkingDir,
		Model: config.Model, PiExtensionPath: config.PiExtensionPath,
	}}, nil
}
func (f *fakeCommanderRuntime) Resume(_ context.Context, session Session, message string) (Session, error) {
	f.followUps = append(f.followUps, message)
	if f.resumeErr != nil {
		return Session{}, f.resumeErr
	}
	return f.delivered(session), nil
}

func (f *fakeCommanderRuntime) Prompt(_ context.Context, session Session, message string) (Session, error) {
	f.prompts = append(f.prompts, message)
	return f.delivered(session), nil
}
func (f *fakeCommanderRuntime) Steer(_ context.Context, session Session, message string) (Session, error) {
	f.steers = append(f.steers, message)
	return f.delivered(session), nil
}
func (f *fakeCommanderRuntime) FollowUp(_ context.Context, session Session, message string) (Session, error) {
	f.followUps = append(f.followUps, message)
	return f.delivered(session), nil
}
func (f *fakeCommanderRuntime) State(context.Context, Session) (State, error) { return f.state, nil }
func (f *fakeCommanderRuntime) Abort(context.Context, Session) error          { f.aborts++; return nil }
func (f *fakeCommanderRuntime) delivered(session Session) Session {
	if f.replacement != nil {
		return *f.replacement
	}
	return session
}

func TestCommanderLifecyclePersistsReconcilesHuskAndRoutesEvents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "pintellect.db")
	projectPath := filepath.Join(root, "project")
	if err := os.Mkdir(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	promptDir := filepath.Join(root, "prompts")
	if err := os.Mkdir(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "AGENTS.md"), []byte("COMMANDER BASELINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, "cmd_commander_project", db.CreateProjectInput{Name: "commander-project", Path: projectPath})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_commander_mission", db.CreateMissionInput{
		ProjectID: projectID, Title: "Commander mission", Objective: "Coordinate a reliable launch",
		AcceptanceCriteria: []domain.Criterion{{Description: "wake for relevant events"}},
		Budget:             domain.MissionBudget{MaxTaskAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeCommanderRuntime{state: StateIdle}
	started, err := (&Starter{Store: store, Runtime: runtime, Prompts: PromptComposer{Dir: promptDir}}).Start(ctx,
		StartRequest{MissionID: mission.ID, Runtime: herdr.RuntimeCodex})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.starts != 1 || started.Session.State != domain.CommanderSessionRunning {
		t.Fatalf("start = %+v calls=%d", started, runtime.starts)
	}
	for _, fragment := range []string{"COMMANDER BASELINE", "Coordinate a reliable launch", "wake for relevant events", "Current mission state snapshot"} {
		if !strings.Contains(runtime.startConfig.Prompt, fragment) {
			t.Errorf("commander prompt omitted %q\n%s", fragment, runtime.startConfig.Prompt)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	observed, err := (&Reconciler{Store: store, Runtime: runtime}).Reconcile(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.ID != started.Session.ID || observed.State != domain.CommanderSessionIdle || observed.LastObservedAt == nil {
		t.Fatalf("restart live observation = %+v", observed)
	}

	runtime.state = StateHusk
	replacement := runtimeSession(observed, projectPath)
	replacement.Herdr.TabID, replacement.Herdr.PaneID = "w1:t2", "w1:p2"
	runtime.replacement = &replacement
	resumed, err := (&Reconciler{Store: store, Runtime: runtime}).Reconcile(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != domain.CommanderSessionRunning || resumed.HerdrPaneID != "w1:p2" ||
		len(runtime.followUps) != 1 || !strings.Contains(runtime.followUps[0], "commander_session_resumed") {
		t.Fatalf("husk resume = %+v followups=%v", resumed, runtime.followUps)
	}
	runtime.replacement = nil

	if _, err := store.CreateSignal(ctx, "cmd_commander_signal", db.CreateSignalInput{
		MissionID: mission.ID, Kind: signalpolicy.SignalDecision, Question: "Which rollout?", Actor: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	woken, err := (&EventWaker{Store: store, Runtime: runtime}).Wake(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.followUps) != 2 || woken.LastEventSequence <= resumed.LastEventSequence {
		t.Fatalf("event wake = %+v followups=%v", woken, runtime.followUps)
	}
	var envelope WakeEnvelope
	if err := json.Unmarshal([]byte(runtime.followUps[1]), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != "mission_events" || envelope.MissionID != mission.ID || len(envelope.Events) != 1 || envelope.Events[0].Type != "signal.created" {
		t.Fatalf("wake envelope = %+v", envelope)
	}
	if _, err := (&EventWaker{Store: store, Runtime: runtime}).Wake(ctx, mission.ID); err != nil {
		t.Fatal(err)
	}
	if len(runtime.followUps) != 2 {
		t.Fatalf("duplicate wake delivered: %v", runtime.followUps)
	}

	runtime.state = StateMissing
	missing, err := (&Reconciler{Store: store, Runtime: runtime}).Reconcile(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if missing.State != domain.CommanderSessionNeedsAttention || !strings.Contains(missing.FailureReason, "missing") {
		t.Fatalf("missing reconciliation = %+v", missing)
	}
}

func TestControllerPreservesPromptSteerAndFollowUpOperations(t *testing.T) {
	store, missionID, promptDir := commanderTestStore(t)
	runtime := &fakeCommanderRuntime{state: StateIdle}
	if _, err := (&Starter{Store: store, Runtime: runtime, Prompts: PromptComposer{Dir: promptDir}}).Start(context.Background(),
		StartRequest{MissionID: missionID, Runtime: herdr.RuntimeClaude}); err != nil {
		t.Fatal(err)
	}
	controller := Controller{Store: store, Runtime: runtime}
	for kind, message := range map[MessageKind]string{MessagePrompt: "operator prompt", MessageSteer: "bounded steer", MessageFollowUp: "queued follow-up"} {
		if _, err := controller.Send(context.Background(), missionID, kind, message); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if len(runtime.prompts) != 1 || len(runtime.steers) != 1 || len(runtime.followUps) != 1 {
		t.Fatalf("prompt=%v steer=%v followup=%v", runtime.prompts, runtime.steers, runtime.followUps)
	}
	events, err := store.MissionEvents(context.Background(), missionID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"commander.prompt": false, "commander.steer": false, "commander.follow_up": false}
	for _, event := range events {
		if _, ok := want[event.Type]; ok {
			want[event.Type] = true
		}
	}
	for eventType, found := range want {
		if !found {
			t.Errorf("missing %s event", eventType)
		}
	}
	if _, err := controller.Send(context.Background(), missionID, MessagePrompt, " "); err == nil {
		t.Fatal("empty commander message accepted")
	}
	stopped, err := controller.Abort(context.Background(), missionID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != domain.CommanderSessionStopped || runtime.aborts != 1 || stopped.StoppedAt == nil {
		t.Fatalf("abort = %+v runtime aborts=%d", stopped, runtime.aborts)
	}
}

func TestRecoveryReplacesMissingCommanderOrStartsFresh(t *testing.T) {
	for _, test := range []struct {
		name       string
		resumeErr  error
		wantStarts int
	}{
		{name: "resume", wantStarts: 1},
		{name: "fresh fallback", resumeErr: errors.New("native session unavailable"), wantStarts: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, missionID, promptDir := commanderTestStore(t)
			runtime := &fakeCommanderRuntime{state: StateMissing, resumeErr: test.resumeErr}
			started, err := (&Starter{Store: store, Runtime: runtime, Prompts: PromptComposer{Dir: promptDir}}).Start(context.Background(),
				StartRequest{MissionID: missionID, Runtime: herdr.RuntimeCodex})
			if err != nil {
				t.Fatal(err)
			}
			replacement := runtimeSession(started.Session, t.TempDir())
			replacement.Herdr.TabID, replacement.Herdr.PaneID = "w1:t2", "w1:p2"
			runtime.replacement = &replacement
			missing, err := (&Reconciler{Store: store, Runtime: runtime}).Reconcile(context.Background(), missionID)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := (&Recovery{Store: store, Runtime: runtime, Prompts: PromptComposer{Dir: promptDir}}).RecoverProject(context.Background(), started.Session.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.ID == missing.ID || recovered.State != domain.CommanderSessionRunning || runtime.starts != test.wantStarts {
				t.Fatalf("recovered=%+v missing=%+v starts=%d", recovered, missing, runtime.starts)
			}
			if test.resumeErr == nil && recovered.HerdrPaneID != "w1:p2" {
				t.Fatalf("resume placement = %+v", recovered)
			}
			if _, err := (&Recovery{Store: store, Runtime: runtime, Prompts: PromptComposer{Dir: promptDir}}).RecoverProject(context.Background(), started.Session.ProjectID); err != nil {
				t.Fatalf("idempotent second recovery: %v", err)
			}
			events, err := store.MissionEvents(context.Background(), missionID)
			if err != nil {
				t.Fatal(err)
			}
			retired, replacementRecorded := false, false
			for _, event := range events {
				retired = retired || event.Type == "commander.session.retired"
				replacementRecorded = replacementRecorded || event.Type == "commander.started"
			}
			if !retired || !replacementRecorded {
				t.Fatalf("recovery events = %+v", events)
			}
		})
	}
}

func commanderTestStore(t *testing.T) (*db.Store, domain.MissionID, string) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	projectPath := t.TempDir()
	projectID, err := store.CreateProject(ctx, "cmd_project_"+domain.CommandID(t.Name()), db.CreateProjectInput{Name: "project-" + t.Name(), Path: projectPath})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_mission_"+domain.CommandID(t.Name()), db.CreateMissionInput{
		ProjectID: projectID, Title: "mission", Objective: "objective", Budget: domain.MissionBudget{MaxTaskAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	promptDir := filepath.Join(t.TempDir(), "prompts")
	if err := os.Mkdir(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "base.md"), []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	return store, mission.ID, promptDir
}
