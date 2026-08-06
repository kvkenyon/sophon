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
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/signals"
	"parallel-intellect/internal/worker"
)

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
