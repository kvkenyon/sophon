package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/flow"
	"sophon/internal/store"
)

// cliFixture wires the hermetic e2e environment: an isolated data home, a
// real Git project with a real bare origin, and fake treehouse/herdr/gh-axi
// binaries backed by shell scripts. Git operations run against the real
// binary; only the delivery repository identity is faked.
type cliFixture struct {
	home      string
	project   string
	git       string
	treehouse string
	herdr     string
	ghAxi     string
	ghLog     string
	herdrLog  string
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv("HERDR_TAB_ID", "")
	t.Setenv("SOPHON_PROMPT_DIR", "")
	root := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary is required for CLI e2e tests")
	}

	origin := filepath.Join(root, "origin.git")
	runCLIGit(t, root, "init", "--bare", origin)
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, project, "init", "-b", "main")
	runCLIGit(t, project, "config", "user.name", "Sophon Test")
	runCLIGit(t, project, "config", "user.email", "test@example.invalid")
	writeCLIFile(t, filepath.Join(project, "base.txt"), "base\n", 0o600)
	runCLIGit(t, project, "add", "base.txt")
	runCLIGit(t, project, "commit", "-m", "base")
	runCLIGit(t, project, "remote", "add", "origin", origin)
	runCLIGit(t, project, "push", "-u", "origin", "main")

	// The fake git wrapper delegates everything to the real binary except the
	// delivery repository identity, which must normalize to host/owner/repo.
	gitBinary := filepath.Join(root, "fake-git")
	writeCLIFile(t, gitBinary, fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" remote get-url origin "*) printf 'git@github.com:sophon/test.git\n'; exit 0 ;;
esac
exec %s "$@"
`, shellQuote(realGit)), 0o700)

	state := filepath.Join(root, "treehouse-state")
	worktrees := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktrees, 0o700); err != nil {
		t.Fatal(err)
	}
	treehouseBinary := filepath.Join(root, "fake-treehouse")
	writeCLIFile(t, treehouseBinary, fmt.Sprintf(`#!/bin/sh
set -eu
state=%s
wtbase=%s
realgit=%s
cmd=${1:-}
if [ $# -gt 0 ]; then shift; fi
case "$cmd" in
  get)
    holder=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --lease-holder) holder=$2; shift 2 ;;
        *) shift ;;
      esac
    done
    n=$(cat "$state/n" 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > "$state/n"
    wt="$wtbase/wt-$n"
    "$realgit" -C "$PWD" worktree add --detach "$wt" >/dev/null 2>&1
    printf '%%s\n%%s\n%%s\n' "$wt" "lease-$n" "$holder" > "$state/lease-$n"
    printf '{"path":"%%s","lease_id":"lease-%%s","lease_holder":"%%s","leased_at":"2026-08-08T00:00:00Z"}\n' "$wt" "$n" "$holder"
    ;;
  status)
    printf '['
    sep=""
    for f in "$state"/lease-*; do
      [ -e "$f" ] || continue
      wt=$(sed -n '1p' "$f")
      lease=$(sed -n '2p' "$f")
      holder=$(sed -n '3p' "$f")
      printf '%%s{"name":"%%s","path":"%%s","status":"leased","lease_id":"%%s","lease_holder":"%%s"}' \
        "$sep" "$(basename "$wt")" "$wt" "$lease" "$holder"
      sep=","
    done
    printf ']\n'
    ;;
  return)
    lease=""
    holder=""
    path=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --if-lease-id) lease=$2; shift 2 ;;
        --if-lease-holder) holder=$2; shift 2 ;;
        --force) shift ;;
        *) path=$1; shift ;;
      esac
    done
    found=0
    for f in "$state"/lease-*; do
      [ -e "$f" ] || continue
      if [ "$(sed -n '1p' "$f")" = "$path" ]; then
        found=1
        [ "$(sed -n '2p' "$f")" = "$lease" ] || exit 1
        [ "$(sed -n '3p' "$f")" = "$holder" ] || exit 1
        rm -f "$f"
      fi
    done
    [ "$found" = 1 ] || exit 1
    ;;
  *) exit 2 ;;
esac
`, shellQuote(state), shellQuote(worktrees), shellQuote(realGit)), 0o700)

	herdrLog := filepath.Join(root, "herdr-calls")
	herdrState := filepath.Join(root, "herdr-state")
	if err := os.MkdirAll(herdrState, 0o700); err != nil {
		t.Fatal(err)
	}
	herdrBinary := filepath.Join(root, "fake-herdr")
	writeCLIFile(t, herdrBinary, fmt.Sprintf(`#!/bin/sh
set -eu
log=%s
state=%s
paneclosed() {
  n=${1##*:p}
  [ "$n" != "$1" ] || return 1
  [ "$n" = 1 ] && return 1
  max=$(cat "$state/tabs" 2>/dev/null || echo 1)
  [ "$n" -le "$max" ] 2>/dev/null || return 0
  grep -qx "w1:t$n" "$state/closed" 2>/dev/null
}
case "$1 $2" in
  "workspace create") printf 'workspace-create cwd=%%s label=%%s\n' "$4" "$6" >> "$log"; printf '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}\n' ;;
  "tab create")
    printf 'tab-create workspace=%%s cwd=%%s label=%%s\n' "$4" "$6" "$8" >> "$log"
    n=$(cat "$state/tabs" 2>/dev/null || echo 1)
    n=$((n + 1))
    echo "$n" > "$state/tabs"
    printf '{"result":{"tab":{"tab_id":"w1:t%%s"},"root_pane":{"pane_id":"w1:p%%s"}}}\n' "$n" "$n"
    ;;
  "tab close") printf 'tab-close %%s\n' "$3" >> "$log"; echo "$3" >> "$state/closed"; printf '{"result":{"ok":true}}\n' ;;
  "pane run") printf 'run %%s %%s\n' "$3" "$4" >> "$log"; printf '{"result":{"ok":true}}\n' ;;
  "tab rename"|"agent rename") printf '{"result":{"ok":true}}\n' ;;
  "pane read") printf 'OpenAI Codex\n' ;;
  "pane get")
    if paneclosed "$3"; then printf '{"error":{"code":"pane_not_found"}}\n'; else printf '{"result":{"pane":{"pane_id":"%%s"}}}\n' "$3"; fi
    ;;
  "agent get")
    if paneclosed "$3"; then printf '{"error":{"code":"pane_not_found"}}\n'; else printf '{"result":{"agent":{"pane_id":"%%s","agent_status":"idle","state_change_seq":1}}}\n' "$3"; fi
    ;;
  "agent prompt") printf 'prompt %%s %%.160s\n' "$3" "$4" >> "$log"; printf '{"result":{"agent":{"pane_id":"%%s","agent_session":{"value":"codex-session-cli"}},"ok":true}}\n' "$3" ;;
  *) exit 2 ;;
esac
`, shellQuote(herdrLog), shellQuote(herdrState)), 0o700)

	ghLog := filepath.Join(root, "gh-axi-calls")
	prState := filepath.Join(root, "gh-axi-pr")
	ghBinary := filepath.Join(root, "fake-gh-axi")
	writeCLIFile(t, ghBinary, fmt.Sprintf(`#!/bin/sh
set -eu
log=%s
prstate=%s
case "${1:-} ${2:-}" in
  "pr list")
    if [ -f "$prstate" ]; then
      printf 'pull_requests[0]{number,url}\n7,https://github.com/sophon/test/pull/7\n'
    else
      printf 'count: 0\n'
    fi
    ;;
  "pr create")
    echo create >> "$log"
    if [ -f "$prstate" ]; then
      exit 1
    fi
    touch "$prstate"
    printf 'number: 7\nurl: https://github.com/sophon/test/pull/7\n'
    ;;
  *) exit 2 ;;
esac
`, shellQuote(ghLog), shellQuote(prState)), 0o700)

	return &cliFixture{home: home, project: project, git: gitBinary,
		treehouse: treehouseBinary, herdr: herdrBinary, ghAxi: ghBinary, ghLog: ghLog, herdrLog: herdrLog}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (f *cliFixture) createMission(t *testing.T, title string) store.Mission {
	t.Helper()
	output := runCLI(t, "mission", "create", "--project", f.project, "--title", title, "--objective", "Exercise the CLI")
	var mission store.Mission
	if err := json.Unmarshal(output, &mission); err != nil {
		t.Fatal(err)
	}
	if mission.ID == "" {
		t.Fatalf("created mission = %+v", mission)
	}
	return mission
}

func (f *cliFixture) createTask(t *testing.T, missionID string, extra ...string) store.Task {
	t.Helper()
	args := append([]string{"task", "create", "--mission", missionID, "--title", "Task"}, extra...)
	output := runCLI(t, args...)
	var task store.Task
	if err := json.Unmarshal(output, &task); err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatalf("created task = %+v", task)
	}
	return task
}

func (f *cliFixture) spawnArgs(taskID string, extra ...string) []string {
	args := []string{"spawn", taskID, "--herdr", f.herdr, "--treehouse", f.treehouse,
		"--git", f.git, "--herdr-session", "fm-lab-cli-test"}
	return append(args, extra...)
}

func (f *cliFixture) spawnTask(t *testing.T, taskID string, extra ...string) store.Spawn {
	t.Helper()
	output := runCLI(t, f.spawnArgs(taskID, extra...)...)
	var spawned store.Spawn
	if err := json.Unmarshal(output, &spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.Pane.PaneID != "w1:p1" || spawned.LeaseID == "" {
		t.Fatalf("spawned = %+v", spawned)
	}
	return spawned
}

// completeWorker simulates the worker for one attempt: it commits a change in
// the attempt worktree, writes the strict result into the attempt directory,
// and publishes it through `sophon worker complete`.
func (f *cliFixture) completeWorker(t *testing.T, missionID, taskID string, attempt int) string {
	t.Helper()
	spawned, err := store.ReadSpawn(missionID, taskID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("change-%d.txt", attempt)
	writeCLIFile(t, filepath.Join(spawned.WorktreePath, name), "change\n", 0o600)
	runCLIGit(t, spawned.WorktreePath, "add", name)
	runCLIGit(t, spawned.WorktreePath, "commit", "-m", "change")
	head := runCLIGit(t, spawned.WorktreePath, "rev-parse", "HEAD")
	resultPath := store.AttemptPath(f.home, missionID, taskID, attempt, "result.json")
	writeCLIFile(t, resultPath, `{"version":1,"status":"completed","summary":"changed",`+
		`"verification":[{"command":"go test ./...","exit_code":0}],"changed_files":["`+name+`"],"risks":[]}`, 0o600)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(resultPath, future, future); err != nil {
		t.Fatal(err)
	}
	output := runCLI(t, "worker", "complete", taskID, "--attempt", fmt.Sprint(attempt),
		"--head-sha", head, "--result", resultPath, "--git", f.git, "--herdr", f.herdr)
	var completion struct {
		ResultSHA256 string `json:"result_sha256"`
	}
	if err := json.Unmarshal(output, &completion); err != nil {
		t.Fatal(err)
	}
	if completion.ResultSHA256 == "" {
		t.Fatalf("worker complete = %s", output)
	}
	return head
}

func (f *cliFixture) verifyComplete(t *testing.T, taskID string) store.Outcome {
	t.Helper()
	output := runCLI(t, "verify-complete", taskID, "--git", f.git, "--treehouse", f.treehouse, "--herdr", f.herdr)
	var outcome store.Outcome
	if err := json.Unmarshal(output, &outcome); err != nil {
		t.Fatal(err)
	}
	return outcome
}

func (f *cliFixture) statusReport(t *testing.T) flow.Report {
	t.Helper()
	output := runCLI(t, "status", "--json", "--herdr", f.herdr, "--herdr-session", "fm-lab-cli-test")
	var report flow.Report
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func (f *cliFixture) taskStatus(t *testing.T, taskID string) store.TaskStatus {
	t.Helper()
	report := f.statusReport(t)
	for _, mission := range report.Missions {
		for _, task := range mission.Tasks {
			if task.Task.ID == taskID {
				return task
			}
		}
	}
	t.Fatalf("task %s missing from status report %+v", taskID, report)
	return store.TaskStatus{}
}

func TestCLIHappyPathMissionToRelease(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Happy path")
	task := fixture.createTask(t, mission.ID, "--validate", "test -f change-1.txt")

	spawned := fixture.spawnTask(t, task.ID)
	if spawned.Attempt != 1 || spawned.Branch == "" {
		t.Fatalf("spawn = %+v", spawned)
	}
	head := fixture.completeWorker(t, mission.ID, task.ID, 1)

	if status := fixture.taskStatus(t, task.ID); status.State != store.StateReady {
		t.Fatalf("status after completion = %+v", status)
	}
	outcome := fixture.verifyComplete(t, task.ID)
	if !strings.EqualFold(outcome.HeadSHA, head) {
		t.Fatalf("outcome = %+v, want head %s", outcome, head)
	}
	validated := runCLI(t, "validate", task.ID, "--git", fixture.git, "--herdr", fixture.herdr)
	var validation store.Validation
	if err := json.Unmarshal(validated, &validation); err != nil {
		t.Fatal(err)
	}
	if !validation.Passed {
		t.Fatalf("validation = %+v", validation)
	}
	deliveredJSON := runCLI(t, "deliver", task.ID, "--confirmed", "--git", fixture.git, "--gh-axi", fixture.ghAxi)
	var delivered store.Delivery
	if err := json.Unmarshal(deliveredJSON, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.State != store.DeliveryDeliveredBranch || !strings.EqualFold(delivered.HeadSHA, head) {
		t.Fatalf("delivered = %+v", delivered)
	}
	releasedJSON := runCLI(t, "release", task.ID, "--treehouse", fixture.treehouse)
	var released store.Release
	if err := json.Unmarshal(releasedJSON, &released); err != nil {
		t.Fatal(err)
	}
	if released.LeaseID != spawned.LeaseID {
		t.Fatalf("released = %+v, want lease %s", released, spawned.LeaseID)
	}
	status := fixture.taskStatus(t, task.ID)
	if status.State != store.StateDelivered || status.Detail != string(store.DeliveryDeliveredBranch) {
		t.Fatalf("final status = %+v", status)
	}
	text := string(runCLI(t, "status", "--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test"))
	if !strings.Contains(text, task.ID+"\tdelivered\t1") {
		t.Fatalf("status text = %q", text)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLICompletionSurvivesWithoutAnySupervisor(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Supervisor death")
	task := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, task.ID)
	fixture.completeWorker(t, mission.ID, task.ID, 1)

	// A fresh invocation with no daemon, supervisor, or commander session must
	// still surface the completed work as ready from records alone.
	if status := fixture.taskStatus(t, task.ID); status.State != store.StateReady {
		t.Fatalf("status = %+v, want ready", status)
	}
	outcome := fixture.verifyComplete(t, task.ID)
	if outcome.Attempt != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLIStaleAttemptRefusalLeavesCurrentAttemptUntouched(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Stale attempt")
	task := fixture.createTask(t, mission.ID)
	first := fixture.spawnTask(t, task.ID)
	second := fixture.spawnTask(t, task.ID, "--retry")
	if second.Attempt != 2 || second.LeaseID == first.LeaseID {
		t.Fatalf("retry spawn = %+v (first %+v)", second, first)
	}

	// The fenced attempt's worker finishes late; its result still publishes
	// into its own attempt directory.
	fixture.completeWorker(t, mission.ID, task.ID, 1)
	if _, err := runCLIErr(t, "verify-complete", task.ID, "--git", fixture.git, "--treehouse", fixture.treehouse); err == nil ||
		!strings.Contains(err.Error(), "fenced non-current attempt") {
		t.Fatalf("verify-complete against stale result error = %v", err)
	}
	if _, err := store.ReadOutcome(mission.ID, task.ID, 2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("attempt 2 outcome err = %v, want not found", err)
	}
	status := fixture.taskStatus(t, task.ID)
	if status.Attempt != 2 || status.State == store.StateDelivered {
		t.Fatalf("status after stale refusal = %+v", status)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLIPullRequestDeliveryIsIdempotent(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "PR idempotency")
	task := fixture.createTask(t, mission.ID, "--delivery", "pr")
	fixture.spawnTask(t, task.ID)
	fixture.completeWorker(t, mission.ID, task.ID, 1)
	fixture.verifyComplete(t, task.ID)

	deliver := func() store.Delivery {
		output := runCLI(t, "deliver", task.ID, "--confirmed", "--git", fixture.git, "--gh-axi", fixture.ghAxi)
		var delivered store.Delivery
		if err := json.Unmarshal(output, &delivered); err != nil {
			t.Fatal(err)
		}
		return delivered
	}
	first := deliver()
	if first.State != store.DeliveryDeliveredPR || first.PRURL != "https://github.com/sophon/test/pull/7" || first.PRNumber != 7 {
		t.Fatalf("first delivery = %+v", first)
	}
	second := deliver()
	if second.PRURL != first.PRURL || second.PRNumber != first.PRNumber || second.State != first.State ||
		!second.DeliveredAt.Equal(*first.DeliveredAt) {
		t.Fatalf("re-delivery diverged: first %+v second %+v", first, second)
	}
	calls, err := os.ReadFile(fixture.ghLog)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(calls), "create"); count != 1 {
		t.Fatalf("gh-axi pr create calls = %d, want 1", count)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLIDeliverRefusals(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Delivery refusals")

	// Delivery requires explicit operator confirmation.
	task := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, task.ID)
	fixture.completeWorker(t, mission.ID, task.ID, 1)
	fixture.verifyComplete(t, task.ID)
	if _, err := runCLIErr(t, "deliver", task.ID, "--git", fixture.git, "--gh-axi", fixture.ghAxi); err == nil ||
		!strings.Contains(err.Error(), "--confirmed") {
		t.Fatalf("unconfirmed deliver error = %v", err)
	}

	// A configured validation must have a passing receipt before delivery.
	gated := fixture.createTask(t, mission.ID, "--validate", "exit 1")
	fixture.spawnTask(t, gated.ID)
	fixture.completeWorker(t, mission.ID, gated.ID, 1)
	fixture.verifyComplete(t, gated.ID)
	if _, err := runCLIErr(t, "deliver", gated.ID, "--confirmed", "--git", fixture.git, "--gh-axi", fixture.ghAxi); err == nil ||
		!strings.Contains(err.Error(), "validation") {
		t.Fatalf("deliver without validation error = %v", err)
	}
	if _, err := runCLIErr(t, "validate", gated.ID, "--git", fixture.git, "--herdr", fixture.herdr); err == nil ||
		!strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("failing validate error = %v", err)
	}
	record, err := store.ReadValidation(mission.ID, gated.ID, 1)
	if err != nil || record.Passed {
		t.Fatalf("validation receipt = %+v, %v", record, err)
	}
	if _, err := runCLIErr(t, "deliver", gated.ID, "--confirmed", "--git", fixture.git, "--gh-axi", fixture.ghAxi); err == nil ||
		!strings.Contains(err.Error(), "validation passed") {
		t.Fatalf("deliver with failed validation error = %v", err)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLIStatusIgnoresWakeLines(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Wake lines")
	task := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, task.ID)

	beforeJSON := runCLI(t, "status", "--json", "--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	beforeText := runCLI(t, "status", "--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	wakePath := store.WakePath(fixture.home, task.ID)
	garbage, err := os.OpenFile(wakePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := garbage.WriteString("total garbage\nnot-a-state delivered\nduplicate\nduplicate\n"); err != nil {
		t.Fatal(err)
	}
	if err := garbage.Close(); err != nil {
		t.Fatal(err)
	}
	afterJSON := runCLI(t, "status", "--json", "--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	afterText := runCLI(t, "status", "--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	if string(beforeJSON) != string(afterJSON) || string(beforeText) != string(afterText) {
		t.Fatalf("wake lines changed status: json %q -> %q, text %q -> %q", beforeJSON, afterJSON, beforeText, afterText)
	}
}

func TestCLICleanStartNeedsNoInit(t *testing.T) {
	fixture := newCLIFixture(t)
	status := runCLI(t, "status", "--herdr", fixture.herdr)
	if strings.TrimSpace(string(status)) != "MISSION\tTASK\tSTATE\tATTEMPT\tDETAIL" {
		t.Fatalf("empty status = %q", status)
	}
	missions := runCLI(t, "mission", "list")
	if strings.TrimSpace(string(missions)) != "ID\tTITLE\tPROJECT\tCREATED" {
		t.Fatalf("empty mission list = %q", missions)
	}
	var listed []store.Mission
	if err := json.Unmarshal(runCLI(t, "mission", "list", "--json"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("empty mission list JSON = %+v", listed)
	}
	mission := fixture.createMission(t, "No init step")
	if mission.ProjectPath != fixture.project {
		t.Fatalf("mission = %+v", mission)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLIPromptCommanderRendersBaseline(t *testing.T) {
	fixture := newCLIFixture(t)
	output := string(runCLI(t, "prompt", "commander"))
	for _, want := range []string{"<!-- commander prompt: commander/AGENTS.md -->", "# Sophon commander", "## Session skill load triggers"} {
		if !strings.Contains(output, want) {
			t.Fatalf("commander prompt omitted %q", want)
		}
	}
	lower := strings.ToLower(output)
	for _, forbidden := range []string{"pintellect", "parallel-intellect", "parallel intellect"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("commander prompt contains forbidden branding %q", forbidden)
		}
	}
	// Skills are materialized per invocation under the data home.
	entries, err := os.ReadDir(filepath.Join(fixture.home, "skills", "commander"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("commander skill sessions = %+v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "skills", "commander", entries[0].Name(), "status", "SKILL.md")); err != nil {
		t.Fatalf("materialized commander skills: %v", err)
	}
	assertNoDatabaseFiles(t, fixture.home)
}

func TestCLIDispatchAndUsageErrors(t *testing.T) {
	fixture := newCLIFixture(t)
	if err := run(context.Background(), nil); err != nil {
		t.Fatalf("bare invocation err = %v", err)
	}
	if output := runCLI(t, "version"); strings.TrimSpace(string(output)) != version {
		t.Fatalf("version = %q", output)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"mystery"}, "unknown command"},
		{"mission subcommand", []string{"mission"}, "expected: sophon mission"},
		{"task subcommand", []string{"task"}, "expected: sophon task"},
		{"worker subcommand", []string{"worker"}, "expected: sophon worker"},
		{"commander subcommand", []string{"commander"}, "expected: sophon commander"},
		{"prompt subcommand", []string{"prompt"}, "expected: sophon prompt"},
		{"mission create missing fields", []string{"mission", "create", "--project", fixture.project}, "required argument is empty"},
		{"task create unknown mission", []string{"task", "create", "--mission", "mission_missing", "--title", "x"}, "not found"},
		{"task create bad delivery", []string{"task", "create", "--mission", "mission_missing", "--title", "x", "--delivery", "gate"}, "unknown delivery mode"},
		{"spawn arity", []string{"spawn", "one", "two"}, "exactly one task ID"},
		{"worker complete arity", []string{"worker", "complete"}, "exactly one task ID"},
		{"worker complete missing result", []string{"worker", "complete", "task_x", "--attempt", "1", "--head-sha", "abc"}, "result path are required"},
		{"verify-complete arity", []string{"verify-complete"}, "exactly one task ID"},
		{"validate arity", []string{"validate"}, "exactly one task ID"},
		{"deliver arity", []string{"deliver"}, "exactly one task ID"},
		{"release arity", []string{"release"}, "exactly one task ID"},
		{"status positional", []string{"status", "extra"}, "does not accept positional"},
		{"send arity", []string{"send", "task_x"}, "task ID and a message"},
		{"mission list positional", []string{"mission", "list", "extra"}, "does not accept positional"},
	} {
		if _, err := runCLIErr(t, test.args...); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}
	var usageErr *exitError
	if err := run(context.Background(), []string{"mission"}); !errors.As(err, &usageErr) || usageErr.code != 2 {
		t.Fatalf("usage exit error = %v", err)
	}
	if got := exitCode(fmt.Errorf("plain")); got != 1 {
		t.Fatalf("plain exit code = %d", got)
	}
}

func TestCLIParseFlagsAcceptsInterspersedPositionals(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Flag order")
	task := fixture.createTask(t, mission.ID)
	// Flags before the positionals parse identically to flags after them.
	spawned := fixture.spawnTask(t, task.ID)
	if spawned.TaskID != task.ID {
		t.Fatalf("spawn = %+v", spawned)
	}
	if _, err := runCLIErr(t, "spawn", task.ID); err == nil || !strings.Contains(err.Error(), "re-run with retry") {
		t.Fatalf("second plain spawn error = %v", err)
	}
	retry := fixture.spawnTask(t, task.ID, "--retry")
	if retry.Attempt != 2 {
		t.Fatalf("retry spawn = %+v", retry)
	}
}

func assertNoDatabaseFiles(t *testing.T, home string) {
	t.Helper()
	var databases []string
	if err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") {
			databases = append(databases, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(databases) != 0 {
		t.Fatalf("database files under data home: %v", databases)
	}
}

func runCLI(t *testing.T, args ...string) []byte {
	t.Helper()
	output, err := runCLIErr(t, args...)
	if err != nil {
		t.Fatalf("sophon %s: %v", strings.Join(args, " "), err)
	}
	return output
}

func runCLIErr(t *testing.T, args ...string) ([]byte, error) {
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
	if closeErr != nil || readErr != nil {
		t.Fatalf("capture CLI output: close=%v read=%v", closeErr, readErr)
	}
	return output, runErr
}

func runCLIGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
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
