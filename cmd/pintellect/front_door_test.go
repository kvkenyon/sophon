package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/knowledge"
)

func TestCLIHomeGuidesMissionCreationWhenEmpty(t *testing.T) {
	output := string(runCLI(t, "home", "--db", filepath.Join(t.TempDir(), "empty.db")))
	for _, fragment := range []string{"Needs Your Attention", "Recently Completed", "Underway", "Up Next", "pintellect mission create --project PATH"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("home output lacks %q:\n%s", fragment, output)
		}
	}
}

func TestHomeCommanderStartConfirmation(t *testing.T) {
	for name, test := range map[string]struct {
		input string
		want  bool
	}{
		"default yes":  {input: "\n", want: true},
		"explicit yes": {input: "yes\n", want: true},
		"decline":      {input: "no\n", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			approved, err := confirmCommanderStart(strings.NewReader(test.input), io.Discard, "codex")
			if err != nil || approved != test.want {
				t.Fatalf("approved=%v err=%v", approved, err)
			}
		})
	}
	if _, err := confirmCommanderStart(strings.NewReader(""), io.Discard, "codex"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("noninteractive confirmation error=%v", err)
	}
}

func TestCLIHomePrintsSnapshotStartsAndAttachesFromOutsideProject(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "project")
	writeCLIFile(t, filepath.Join(projectPath, "prompts", "commander", "AGENTS.md"), "PROJECT COMMANDER PROMPT\n", 0o600)
	dbPath := filepath.Join(root, "pintellect.db")
	store, err := db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(context.Background(), "home_project", db.CreateProjectInput{Name: "home", Path: projectPath})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(context.Background(), "home_mission", db.CreateMissionInput{
		ProjectID: projectID, Title: "Front door", Objective: "Start one conversational commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(context.Background(), "home_task", db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "Queued work",
		Objective: "Appear in home", DeliveryMode: domain.DeliveryBranch,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	callLog := filepath.Join(root, "herdr.calls")
	herdrBinary := filepath.Join(root, "fake-herdr-home")
	herdrScript := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> %q
case "$1 $2" in
  "workspace create") printf '{"result":{"workspace":{"workspace_id":"hw1"},"tab":{"tab_id":"hw1:t1"},"root_pane":{"pane_id":"hw1:p1"}}}\n' ;;
  "pane run"|"agent rename") printf '{"result":{"ok":true}}\n' ;;
  "pane read") printf 'OpenAI Codex\n' ;;
  "pane get") printf '{"result":{"pane":{"pane_id":"hw1:p1"}}}\n' ;;
  "agent get") printf '{"result":{"agent":{"agent":"codex","pane_id":"hw1:p1","agent_status":"idle","state_change_seq":1,"agent_session":{"value":"home-codex-session"}}}}\n' ;;
  "agent prompt") printf '{"result":{"agent":{"agent":"codex","pane_id":"hw1:p1","agent_session":{"value":"home-codex-session"}},"ok":true}}\n' ;;
  "agent attach") printf 'attached home commander\n' ;;
  *) exit 2 ;;
esac
`, callLog)
	writeCLIFile(t, herdrBinary, herdrScript, 0o700)

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDir) })

	output := string(runCLI(t, "home", "--mission", string(mission.ID), "--db", dbPath, "--yes",
		"--herdr", herdrBinary, "--herdr-session", "fm-lab-home"))
	statusAt, attachAt := strings.Index(output, "Mission: Front door"), strings.Index(output, "Attaching:")
	if statusAt < 0 || attachAt < 0 || statusAt >= attachAt || !strings.Contains(output, "Up Next") ||
		!strings.Contains(output, "attached home commander") {
		t.Fatalf("home output did not snapshot then attach:\n%s", output)
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "agent attach hw1:p1 --session fm-lab-home") {
		t.Fatalf("home attach call missing session guard:\n%s", calls)
	}
	store, err = db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session, err := store.CommanderSession(context.Background(), mission.ID)
	if err != nil || session.HerdrSessionName != "fm-lab-home" || session.AgentSessionID != "home-codex-session" {
		t.Fatalf("home commander=%+v err=%v", session, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	output = string(runCLI(t, "home", "--mission", string(mission.ID), "--db", dbPath, "--herdr", herdrBinary))
	if !strings.Contains(output, "Attaching:") || strings.Contains(output, "Commander started:") {
		t.Fatalf("existing commander home path=%s", output)
	}
	calls, err = os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "workspace create") != 1 || strings.Count(string(calls), "agent attach hw1:p1 --session fm-lab-home") != 2 {
		t.Fatalf("home should start once and attach twice:\n%s", calls)
	}
	store, err = db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
}

func TestCLIKnowledgeLifecyclePreservesProposalProvenance(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, "knowledge_project", db.CreateProjectInput{Name: "knowledge", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "knowledge_mission", db.CreateMissionInput{ProjectID: projectID, Title: "Learn", Objective: "Review candidates"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "knowledge_task", db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskScout, Title: "Find pattern", Objective: "Produce evidence", DeliveryMode: domain.DeliveryBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	propose := func(command, kind, content string) knowledge.Entry {
		entry, err := store.ProposeKnowledge(ctx, domain.CommandID(command), db.ProposeKnowledgeInput{
			ProjectID: projectID, MissionID: &mission.ID, Scope: knowledge.ScopeLearned,
			Kind: kind, Content: content, CreatedBy: "worker-1", Origin: knowledge.OriginAgent,
			TriggerTaskID: &task.ID, Confidence: .8,
		})
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	old := propose("knowledge_old", "test-pattern", "Reset the fixture.")
	replacement := propose("knowledge_replacement", "test-pattern", "Reset the fixture after crash recovery.")
	rejected := propose("knowledge_rejected", "weak-pattern", "Assume retries always pass.")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runCLI(t, "knowledge", "promote", string(old.ID), "--db", dbPath)
	runCLI(t, "knowledge", "promote", string(replacement.ID), "--db", dbPath)
	runCLI(t, "knowledge", "reject", string(rejected.ID), "--db", dbPath)
	runCLI(t, "knowledge", "supersede", string(old.ID), "--by", string(replacement.ID), "--db", dbPath)
	var active []knowledge.Entry
	if err := json.Unmarshal(runCLI(t, "knowledge", "list", "--status", "active", "--db", dbPath, "--json"), &active); err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != replacement.ID || active[0].CreatedBy != "worker-1" ||
		active[0].Origin != knowledge.OriginAgent || active[0].TriggerTaskID == nil || *active[0].TriggerTaskID != task.ID {
		t.Fatalf("active knowledge=%+v", active)
	}
	store, err = db.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldAfter, err := store.Knowledge(ctx, old.ID)
	if err != nil || oldAfter.Status != knowledge.StatusSuperseded || oldAfter.SupersededBy == nil || *oldAfter.SupersededBy != replacement.ID {
		t.Fatalf("superseded=%+v err=%v", oldAfter, err)
	}
	rejectedAfter, err := store.Knowledge(ctx, rejected.ID)
	if err != nil || rejectedAfter.Status != knowledge.StatusRejected {
		t.Fatalf("rejected=%+v err=%v", rejectedAfter, err)
	}
	events, err := store.MissionEvents(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundOperatorPromotion := false
	for _, event := range events {
		if event.Type == "knowledge.active" && event.Actor == "operator" {
			foundOperatorPromotion = true
		}
	}
	if !foundOperatorPromotion {
		t.Fatalf("knowledge promotion provenance missing from events: %+v", events)
	}
}
