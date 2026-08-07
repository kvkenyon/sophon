package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/knowledge"
)

func TestDetectHerdrSessionMatrix(t *testing.T) {
	for _, test := range []struct {
		name     string
		sessions string
		env      map[string]string
		want     string
		wantErr  string
	}{
		{name: "environment", sessions: `{"sessions":[{"name":"one","running":true},{"name":"two","running":true}]}`, env: map[string]string{"HERDR_SESSION": "two"}, want: "two"},
		{name: "single running", sessions: `{"sessions":[{"name":"only","running":true},{"name":"old","running":false}]}`, want: "only"},
		{name: "ambiguous", sessions: `{"sessions":[{"name":"one","running":true},{"name":"two","running":true}]}`, wantErr: "multiple sessions are running"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "fake-herdr")
			writeCLIFile(t, binary, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", test.sessions), 0o700)
			got, err := detectHerdrSession(context.Background(), binary, "", func(key string) string { return test.env[key] })
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("session=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("session=%q err=%v", got, err)
			}
		})
	}
}

func TestProjectRootFromCWD(t *testing.T) {
	root := t.TempDir()
	runCLIGit(t, root, "init")
	nested := filepath.Join(root, "nested", "directory")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	got, err := projectRootFromCWD(context.Background())
	want, evalErr := filepath.EvalSymlinks(root)
	if err != nil || evalErr != nil || got != want {
		t.Fatalf("root=%q err=%v want=%q eval_err=%v", got, err, want, evalErr)
	}
}

func TestProjectRootFromCWDRejectsNonRepositoryPlainly(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if _, err := projectRootFromCWD(context.Background()); err == nil || !strings.Contains(err.Error(), "not inside a Git repository") {
		t.Fatalf("non-repository error=%v", err)
	}
}

func TestCLIHomeFromRepositoryStartsIntakeAndReattachesIdempotently(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, projectPath, "init")
	writeCLIFile(t, filepath.Join(projectPath, "prompts", "commander", "AGENTS.md"), "PROJECT COMMANDER PROMPT\n", 0o600)
	dbPath := filepath.Join(root, "pintellect.db")

	callLog := filepath.Join(root, "herdr.calls")
	herdrBinary := filepath.Join(root, "fake-herdr-home")
	herdrScript := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> %q
case "$1 $2" in
	"session list") printf '{"sessions":[{"name":"fm-lab-home","running":true,"socket_path":"/tmp/lab.sock"}]}\n' ;;
  "workspace create") printf '{"result":{"workspace":{"workspace_id":"hw1"},"tab":{"tab_id":"hw1:t1"},"root_pane":{"pane_id":"hw1:p1"}}}\n' ;;
  "pane run"|"agent rename") printf '{"result":{"ok":true}}\n' ;;
  "pane read") printf 'OpenAI Codex\n' ;;
  "pane get") printf '{"result":{"pane":{"pane_id":"hw1:p1"}}}\n' ;;
  "agent get") printf '{"result":{"agent":{"agent":"codex","pane_id":"hw1:p1","agent_status":"idle","state_change_seq":1,"agent_session":{"value":"home-codex-session"}}}}\n' ;;
  "agent prompt") printf '{"result":{"agent":{"agent":"codex","pane_id":"hw1:p1","agent_session":{"value":"home-codex-session"}},"ok":true}}\n' ;;
	"agent focus") printf '{"result":{"ok":true}}\n' ;;
  "agent attach") printf 'attached home commander\n' ;;
  *) exit 2 ;;
esac
`, callLog)
	writeCLIFile(t, herdrBinary, herdrScript, 0o700)

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDir) })
	t.Setenv("HERDR_SESSION", "fm-lab-home")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_CLIENT_SOCKET_PATH", "")

	output := string(runCLI(t, "home", "--db", dbPath, "--herdr", herdrBinary))
	if !strings.Contains(output, "intake mode") || !strings.Contains(output, "attached home commander") {
		t.Fatalf("first home output:\n%s", output)
	}
	output = string(runCLI(t, "home", "--db", dbPath, "--herdr", herdrBinary))
	if !strings.Contains(output, "attached home commander") || strings.Contains(output, "intake mode") {
		t.Fatalf("second home output:\n%s", output)
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "workspace create") != 1 ||
		strings.Count(string(calls), "agent attach hw1:p1 --session fm-lab-home") != 2 ||
		strings.Count(string(calls), "agent focus hw1:p1 --session fm-lab-home") != 2 {
		t.Fatalf("home should create once, focus and attach twice:\n%s", calls)
	}
	store, err := db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := store.Projects(context.Background())
	physicalProject, evalErr := filepath.EvalSymlinks(projectPath)
	if err != nil || evalErr != nil || len(projects) != 1 || projects[0].Path != physicalProject {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
	session, err := store.ProjectCommanderSession(context.Background(), projects[0].ID)
	if err != nil || session.MissionID != "" || session.HerdrSessionName != "fm-lab-home" {
		t.Fatalf("intake commander=%+v err=%v", session, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var mission domain.Mission
	if err := json.Unmarshal(runCLI(t, "mission", "create", "--project", projects[0].Path, "--title", "Natural intake", "--objective", "Execute the described work", "--acceptance", "The result is verified", "--db", dbPath), &mission); err != nil {
		t.Fatal(err)
	}
	store, err = db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bound, err := store.ProjectCommanderSession(context.Background(), projects[0].ID)
	if err != nil || bound.ID != session.ID || bound.MissionID != mission.ID || mission.CommanderSessionID != session.ID {
		t.Fatalf("bound commander=%+v mission=%+v err=%v", bound, mission, err)
	}
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
