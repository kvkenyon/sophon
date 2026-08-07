package db

import (
	"context"
	"testing"

	"sophon/internal/domain"
	"sophon/internal/signals"
)

func TestMissionCancelClosesSignalsStopsDeadCommanderAndRejectsTasks(t *testing.T) {
	ctx := context.Background()
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	if _, err := store.CreateSignal(ctx, "mission_cancel_signal", CreateSignalInput{MissionID: mission.ID, Kind: signals.SignalDecision, Question: "Proceed?", Actor: "commander"}); err != nil {
		t.Fatal(err)
	}
	session, err := store.RecordCommanderSession(ctx, "mission_cancel_commander", RecordCommanderSessionInput{MissionID: mission.ID, Actor: "operator", Session: domain.CommanderSession{
		ID: "csn_mission_cancel", Runtime: "codex", HerdrSessionName: "test", HerdrWorkspaceID: "w", HerdrTabID: "t", HerdrPaneID: "p", HerdrAgentName: "agent", AgentSessionID: "session",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveCommanderSession(ctx, "mission_cancel_commander_dead", ObserveCommanderSessionInput{SessionID: session.ID, MissionID: mission.ID, ExpectedState: session.State, ExpectedVersion: session.Version, ObservedState: domain.CommanderSessionFailed, Actor: "recovery"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginMissionCancel(ctx, "mission_cancel_begin", mission.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.FinishMissionCancel(ctx, "mission_cancel", mission.ID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != domain.MissionCancelled {
		t.Fatalf("mission=%+v", cancelled)
	}
	items, err := store.Signals(ctx, ListSignalsFilter{MissionID: mission.ID})
	if err != nil || len(items) != 1 || items[0].Status != signals.SignalResolved || items[0].Answer == nil || *items[0].Answer != operatorCancellationNote {
		t.Fatalf("signals=%+v err=%v", items, err)
	}
	if _, err := store.CreateTask(ctx, "mission_cancel_new_task", CreateTaskInput{MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "forbidden", Objective: "forbidden", DeliveryMode: domain.DeliveryBranch}); err == nil {
		t.Fatal("cancelled mission accepted a task")
	}
	repeated, err := store.FinishMissionCancel(ctx, "mission_cancel", mission.ID, "operator")
	if err != nil || repeated.ID != cancelled.ID {
		t.Fatalf("repeat=%+v err=%v", repeated, err)
	}
	if _, err := store.BeginMissionCancel(ctx, "mission_cancel_wrong_state", mission.ID, "operator"); err != nil {
		// The terminal begin path is intentionally a harmless no-op.
		t.Fatal(err)
	}
}
