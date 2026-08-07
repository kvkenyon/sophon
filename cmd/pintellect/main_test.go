package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/delivery"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/signals"
	statusview "parallel-intellect/internal/status"
	"parallel-intellect/internal/worker"
)

func TestCLIStatusSnapshotSectionsAndJSON(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, "status_project", db.CreateProjectInput{Name: "status", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "status_mission", db.CreateMissionInput{ProjectID: projectID, Title: "Status", Objective: "exercise snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	makeTask := func(name string) domain.Task {
		task, err := store.CreateTask(ctx, domain.CommandID("status_task_"+name), db.CreateTaskInput{MissionID: mission.ID, Kind: domain.TaskImplementation, Title: name, Objective: name, DeliveryMode: domain.DeliveryBranch})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	transition := func(task domain.Task, to domain.TaskState) domain.Task {
		updated, err := store.TransitionTask(ctx, domain.CommandID("status_transition_"+string(task.ID)+"_"+string(to)), db.TransitionTaskInput{TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State, ExpectedVersion: task.Version, To: to, Actor: "commander"})
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}
	blocked := makeTask("blocked")
	blocked = transition(blocked, domain.TaskProvisioning)
	blocked = transition(blocked, domain.TaskStarting)
	blocked = transition(blocked, domain.TaskRunning)
	_ = transition(blocked, domain.TaskBlocked)
	completed := makeTask("completed")
	completed = transition(completed, domain.TaskProvisioning)
	completed = transition(completed, domain.TaskStarting)
	completed = transition(completed, domain.TaskRunning)
	completed = transition(completed, domain.TaskCollecting)
	_ = transition(completed, domain.TaskReady)
	underway := makeTask("underway")
	_ = transition(underway, domain.TaskProvisioning)
	queued := makeTask("queued")
	if _, err := store.CreateSignal(ctx, "status_signal", db.CreateSignalInput{MissionID: mission.ID, TaskID: &queued.ID, Kind: signals.SignalDecision, Question: "Choose a path", Actor: "commander"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var snapshot statusview.Snapshot
	if err := json.Unmarshal(runCLI(t, "status", "--mission", string(mission.ID), "--db", dbPath, "--json"), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Mission == nil || snapshot.Mission.ID != mission.ID {
		t.Fatalf("mission = %+v", snapshot.Mission)
	}
	if len(snapshot.NeedsYourAttention.Tasks) != 1 || snapshot.NeedsYourAttention.Tasks[0].Title != "blocked" || len(snapshot.NeedsYourAttention.Signals) != 1 {
		t.Fatalf("attention = %+v", snapshot.NeedsYourAttention)
	}
	if len(snapshot.RecentlyCompleted) != 1 || snapshot.RecentlyCompleted[0].Title != "completed" {
		t.Fatalf("completed = %+v", snapshot.RecentlyCompleted)
	}
	if len(snapshot.Underway) != 1 || snapshot.Underway[0].Title != "underway" {
		t.Fatalf("underway = %+v", snapshot.Underway)
	}
	if len(snapshot.UpNext) != 1 || snapshot.UpNext[0].Title != "queued" {
		t.Fatalf("up next = %+v", snapshot.UpNext)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(runCLI(t, "status", "--mission", string(mission.ID), "--db", dbPath, "--json"), &shape); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"needs_your_attention", "recently_completed", "underway", "up_next"} {
		if _, ok := shape[key]; !ok {
			t.Errorf("JSON lacks %q: %s", key, shape)
		}
	}
	human := string(runCLI(t, "status", "--mission", string(mission.ID), "--db", dbPath))
	for _, section := range []string{"Needs Your Attention", "Recently Completed", "Underway", "Up Next"} {
		if !strings.Contains(human, section) {
			t.Errorf("human status lacks %q: %s", section, human)
		}
	}
}

func TestCLIStatusEmptyMission(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	var snapshot statusview.Snapshot
	if err := json.Unmarshal(runCLI(t, "status", "--db", dbPath, "--json"), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Mission != nil || len(snapshot.NeedsYourAttention.Tasks) != 0 || len(snapshot.NeedsYourAttention.Signals) != 0 || len(snapshot.RecentlyCompleted) != 0 || len(snapshot.Underway) != 0 || len(snapshot.UpNext) != 0 {
		t.Fatalf("empty snapshot = %+v", snapshot)
	}
}

func TestCLIMissionDigestRegeneratesArtifact(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), "cmd_digest_project", db.CreateProjectInput{Name: "digest", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(context.Background(), "cmd_digest_mission", db.CreateMissionInput{ProjectID: project, Title: "Digest", Objective: "Compact mission context."})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	output := string(runCLI(t, "mission", "digest", string(mission.ID), "--db", dbPath))
	if !strings.Contains(output, "## Objective") || !strings.Contains(output, "Compact mission context.") {
		t.Fatalf("digest output=%s", output)
	}
	store, err = db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifact, err := store.LatestMissionDigest(context.Background(), mission.ID)
	if err != nil || artifact.Kind != "mission.digest" {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestCLISignalListInspectAndResolveJSON(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, "cmd_cli_signal_project", db.CreateProjectInput{
		Name: "signals", Path: filepath.Join(t.TempDir(), "signals"),
	})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_cli_signal_mission", db.CreateMissionInput{
		ProjectID: projectID, Title: "Signals", Objective: "Exercise signal CLI",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSignal(ctx, "cmd_cli_signal_create", db.CreateSignalInput{
		MissionID: mission.ID, Kind: signals.SignalDecision,
		Question: "Which path?", Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var listed []signals.Signal
	if err := json.Unmarshal(runCLI(t, "signal", "list", "--db", dbPath, "--json"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed signals = %+v", listed)
	}
	var inspected signals.Signal
	if err := json.Unmarshal(runCLI(t, "signal", "inspect", string(created.ID), "--db", dbPath, "--json"), &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected.Question != created.Question {
		t.Fatalf("inspected signal = %+v", inspected)
	}
	var resolved signals.Signal
	if err := json.Unmarshal(runCLI(t, "signal", "resolve", string(created.ID), "--db", dbPath,
		"--answer", "Take the strict path.", "--json"), &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Status != signals.SignalResolved || resolved.Answer == nil || *resolved.Answer != "Take the strict path." {
		t.Fatalf("resolved signal = %+v", resolved)
	}
}

func TestCLIOneCodexWorkerVerticalSliceWithHermeticAdapters(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, repo, "init", "-b", "task-slice")
	runCLIGit(t, repo, "config", "user.name", "Parallel Intellect Test")
	runCLIGit(t, repo, "config", "user.email", "test@example.invalid")
	writeCLIFile(t, filepath.Join(repo, "base.txt"), "base\n", 0o600)
	runCLIGit(t, repo, "add", "base.txt")
	runCLIGit(t, repo, "commit", "-m", "base")

	dbPath := filepath.Join(root, "state.db")
	taskFiles := filepath.Join(root, "task-files")
	missionJSON := runCLI(t, "mission", "create", "--db", dbPath, "--project", repo,
		"--title", "Mission", "--objective", "Exercise the vertical slice")
	var mission domain.Mission
	if err := json.Unmarshal(missionJSON, &mission); err != nil {
		t.Fatal(err)
	}
	taskJSON := runCLI(t, "task", "create", string(mission.ID), "--db", dbPath,
		"--title", "Task", "--objective", "Make one committed change", "--acceptance", "Task reaches ready")
	var task domain.Task
	if err := json.Unmarshal(taskJSON, &task); err != nil {
		t.Fatal(err)
	}

	leaseState := filepath.Join(root, "lease-holder")
	treehouseBinary := filepath.Join(root, "fake-treehouse")
	treehouseScript := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
  get)
    shift
    holder=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--lease-holder" ]; then holder=$2; shift 2; else shift; fi
    done
    printf '%%s' "$holder" > %q
    printf '{"path":%q,"lease_id":"lease-cli","lease_holder":"%%s","leased_at":"2026-08-06T12:00:00Z"}\n' "$holder"
    ;;
  status)
    holder=$(sed -n '1p' %q)
    printf '[{"name":"slice","path":%q,"status":"leased","lease_id":"lease-cli","lease_holder":"%%s"}]\n' "$holder"
    ;;
  return) exit 0 ;;
  *) exit 2 ;;
esac
`, leaseState, repo, leaseState, repo)
	writeCLIFile(t, treehouseBinary, treehouseScript, 0o700)

	herdrBinary := filepath.Join(root, "fake-herdr")
	herdrScript := `#!/bin/sh
set -eu
case "$1 $2" in
  "workspace create") printf '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}\n' ;;
  "pane run"|"agent rename") printf '{"result":{"ok":true}}\n' ;;
  "pane read") printf 'OpenAI Codex\n' ;;
  "pane get") printf '{"result":{"pane":{"pane_id":"w1:p1"}}}\n' ;;
  "agent get") printf '{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle","state_change_seq":1}}}\n' ;;
  "agent prompt") printf '{"result":{"agent":{"pane_id":"w1:p1","agent_session":{"value":"codex-session-cli"}},"ok":true}}\n' ;;
  *) exit 2 ;;
esac
`
	writeCLIFile(t, herdrBinary, herdrScript, 0o700)

	startedJSON := runCLI(t, "task", "start", string(task.ID), "--db", dbPath,
		"--treehouse", treehouseBinary, "--herdr", herdrBinary, "--herdr-session", "fm-lab-cli-test",
		"--task-files", taskFiles, "--validate", "go test ./...")
	var started worker.StartResult
	if err := json.Unmarshal(startedJSON, &started); err != nil {
		t.Fatal(err)
	}
	if started.Task.State != domain.TaskRunning || started.WorkerSession.HerdrPaneID != "w1:p1" {
		t.Fatalf("started slice = %+v", started)
	}

	writeCLIFile(t, filepath.Join(repo, "change.txt"), "change\n", 0o600)
	runCLIGit(t, repo, "add", "change.txt")
	runCLIGit(t, repo, "commit", "-m", "change")
	head := runCLIGit(t, repo, "rev-parse", "HEAD")
	resultPath := filepath.Join(taskFiles, string(task.ID), "1", "result.json")
	writeCLIFile(t, resultPath, `{"version":1,"status":"completed","summary":"changed","verification":[{"command":"go test ./...","exit_code":0}],"changed_files":["change.txt"],"risks":[]}`, 0o600)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(resultPath, future, future); err != nil {
		t.Fatal(err)
	}
	readyJSON := runCLI(t, "worker", "complete", string(task.ID), "--db", dbPath,
		"--treehouse", treehouseBinary, "--task-files", taskFiles, "--attempt", "1",
		"--head-sha", head, "--result", resultPath)
	var ready domain.Task
	if err := json.Unmarshal(readyJSON, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.TaskReady {
		t.Fatalf("completed slice task = %+v", ready)
	}
	deliveredJSON := runCLI(t, "task", "deliver", string(task.ID), "--db", dbPath,
		"--command-id", "cmd_cli_deliver")
	var delivered delivery.Result
	if err := json.Unmarshal(deliveredJSON, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Task.State != domain.TaskDeliveredBranch || delivered.Delivery.HeadSHA != head {
		t.Fatalf("delivered branch = %+v", delivered)
	}
	releasedJSON := runCLI(t, "task", "release", string(task.ID), "--db", dbPath,
		"--treehouse", treehouseBinary, "--command-id", "cmd_cli_release")
	var released domain.TreehouseLease
	if err := json.Unmarshal(releasedJSON, &released); err != nil {
		t.Fatal(err)
	}
	if released.State != domain.TreehouseLeaseReleased || released.LeaseID != "lease-cli" {
		t.Fatalf("released branch lease = %+v", released)
	}
}

func TestCLICommanderStartPromptAttachAndStatus(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "pintellect.db")
	projectPath := filepath.Join(root, "project")
	promptDir := filepath.Join(root, "prompts", "commander")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(promptDir, "AGENTS.md"), "COMMANDER CLI BASELINE\n", 0o600)

	store, err := db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(context.Background(), "cmd_cli_commander_project", db.CreateProjectInput{Name: "cli-commander", Path: projectPath})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(context.Background(), "cmd_cli_commander_mission", db.CreateMissionInput{
		ProjectID: projectID, Title: "CLI commander", Objective: "receive mission context",
		AcceptanceCriteria: []domain.Criterion{{Description: "accept operator steer"}},
		Budget:             domain.MissionBudget{MaxTaskAttempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	herdrBinary := filepath.Join(root, "fake-herdr-commander")
	herdrScript := `#!/bin/sh
set -eu
case "$1 $2" in
  "workspace create") printf '{"result":{"workspace":{"workspace_id":"cw1"},"tab":{"tab_id":"cw1:t1"},"root_pane":{"pane_id":"cw1:p1"}}}\n' ;;
  "pane run"|"agent rename") printf '{"result":{"ok":true}}\n' ;;
  "pane read") printf 'OpenAI Codex\n' ;;
  "pane get") printf '{"result":{"pane":{"pane_id":"cw1:p1"}}}\n' ;;
  "agent get") printf '{"result":{"agent":{"agent":"codex","pane_id":"cw1:p1","agent_status":"idle","state_change_seq":1,"agent_session":{"value":"commander-codex-session"}}}}\n' ;;
  "agent prompt") printf '{"result":{"agent":{"agent":"codex","pane_id":"cw1:p1","agent_session":{"value":"commander-codex-session"}},"ok":true}}\n' ;;
  *) exit 2 ;;
esac
`
	writeCLIFile(t, herdrBinary, herdrScript, 0o700)

	startedJSON := runCLI(t, "commander", "start", "--agent", "codex", "--mission", string(mission.ID),
		"--db", dbPath, "--herdr", herdrBinary, "--herdr-session", "fm-lab-cli-commander", "--prompt-dir", promptDir)
	var started domain.CommanderSession
	if err := json.Unmarshal(startedJSON, &started); err != nil {
		t.Fatal(err)
	}
	if started.MissionID != mission.ID || started.AgentSessionID != "commander-codex-session" || started.State != domain.CommanderSessionRunning {
		t.Fatalf("commander start = %+v", started)
	}

	promptedJSON := runCLI(t, "commander", "prompt", "Please acknowledge the steer", "--mission", string(mission.ID),
		"--db", dbPath, "--herdr", herdrBinary)
	var prompted domain.CommanderSession
	if err := json.Unmarshal(promptedJSON, &prompted); err != nil {
		t.Fatal(err)
	}
	if prompted.ID != started.ID || prompted.Version <= started.Version {
		t.Fatalf("commander prompt = %+v", prompted)
	}

	statusJSON := runCLI(t, "commander", "status", "--mission", string(mission.ID), "--db", dbPath, "--json")
	var status domain.CommanderSession
	if err := json.Unmarshal(statusJSON, &status); err != nil || status.ID != started.ID {
		t.Fatalf("commander status = %+v, %v", status, err)
	}
	attachJSON := runCLI(t, "commander", "attach", "--mission", string(mission.ID), "--db", dbPath, "--herdr", herdrBinary, "--json")
	var attach struct {
		Attach []string `json:"attach"`
	}
	if err := json.Unmarshal(attachJSON, &attach); err != nil || len(attach.Attach) < 6 || attach.Attach[len(attach.Attach)-1] != "fm-lab-cli-commander" {
		t.Fatalf("commander attach = %+v, %v", attach, err)
	}
	statusText := string(runCLI(t, "commander", "status", "--mission", string(mission.ID), "--db", dbPath))
	if !strings.Contains(statusText, string(started.ID)+"\trunning\tfm-lab-cli-commander\tcw1:p1") {
		t.Fatalf("commander status text = %q", statusText)
	}
	attachText := string(runCLI(t, "commander", "attach", "--mission", string(mission.ID), "--db", dbPath, "--herdr", herdrBinary))
	if !strings.Contains(attachText, herdrBinary+" agent attach cw1:p1 --session fm-lab-cli-commander") {
		t.Fatalf("commander attach text = %q", attachText)
	}
}

func TestCLICommanderValidation(t *testing.T) {
	for _, args := range [][]string{
		{"commander", "start", "--agent", "codex", "--mission", "msn_1"},
		{"commander", "start", "--agent", "unknown", "--mission", "msn_1", "--herdr-session", "lab"},
		{"commander", "prompt", "--mission", "msn_1"},
		{"commander", "status", "unexpected"},
		{"commander", "attach", "unexpected"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("pintellect %s unexpectedly succeeded", strings.Join(args, " "))
		}
	}
}

func runCLI(t *testing.T, args ...string) []byte {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := run(context.Background(), args)
	closeErr := writer.Close()
	os.Stdout = original
	output, readErr := io.ReadAll(reader)
	reader.Close()
	if runErr != nil {
		t.Fatalf("pintellect %s: %v", strings.Join(args, " "), runErr)
	}
	if closeErr != nil || readErr != nil {
		t.Fatalf("capture CLI output: close=%v read=%v", closeErr, readErr)
	}
	return output
}

func runCLIGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeCLIFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
