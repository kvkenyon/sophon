package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/domain"
	"sophon/internal/flow"
	"sophon/internal/store"
)

// TestCLIWorkerEnvironmentCarriesAssignedDataHome reproduces the captain's
// data-home divergence: the commander selected a non-default SOPHON_DATA_HOME,
// the brief pinned the result path under it, but the worker's completion
// command resolved the default store and failed with "store record not found".
// The spawned runtime must receive the exact resolved data home and the
// brief's completion command must pin it, so a runtime that drops inherited
// environment still publishes to the assigned store.
func TestCLIWorkerEnvironmentCarriesAssignedDataHome(t *testing.T) {
	fixture := newCLIFixture(t)
	// A non-default data home with a space, as in the observed smoke run.
	home := filepath.Join(t.TempDir(), "sophon smoke home")
	t.Setenv("SOPHON_DATA_HOME", home)
	fixture.home = home

	mission := fixture.createMission(t, "Data home divergence")
	task := fixture.createTask(t, mission.ID)
	spawned := fixture.spawnTask(t, task.ID)

	// The Herdr launch boundary must have received the exact resolved data
	// home for every supported runtime launch, shell-quoted, with no ambient
	// fallback.
	launch := readLogLines(t, fixture.herdrLog)
	wantEnv := "SOPHON_DATA_HOME='" + home + "'"
	if !strings.Contains(launch, "run w1:p1 "+wantEnv+" codex ") {
		t.Fatalf("worker launch command = %q, want it to begin %q", launch, wantEnv)
	}

	// The generated brief must pin the completion command to the same home.
	briefPath := store.AttemptPath(home, mission.ID, task.ID, 1, "brief.md")
	brief, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), wantEnv+" sophon worker complete "+task.ID) {
		t.Fatalf("brief completion command not pinned to the assigned data home:\n%s", brief)
	}
	if !strings.Contains(string(brief), wantEnv+" sophon worker progress "+task.ID+" --attempt 1") {
		t.Fatalf("brief progress command not pinned to the assigned data home:\n%s", brief)
	}

	// The full worker-completion path with a scrubbed environment: the worker
	// inherits nothing (no SOPHON_DATA_HOME, empty HOME), so only the brief's
	// own command can select the assigned store. Before the fix this failed
	// with "store record not found: task ...".
	name := "change-1.txt"
	writeCLIFile(t, filepath.Join(spawned.WorktreePath, name), "change\n", 0o600)
	runCLIGit(t, spawned.WorktreePath, "add", name)
	runCLIGit(t, spawned.WorktreePath, "commit", "-m", "change")
	resultPath := store.AttemptPath(home, mission.ID, task.ID, 1, store.CompletionSubmissionName)
	writeCLIFile(t, resultPath, `{"version":1,"status":"completed","summary":"changed",`+
		`"verification":[{"command":"go test ./...","exit_code":0}],"changed_files":["`+name+`"],"risks":[]}`, 0o600)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(resultPath, future, future); err != nil {
		t.Fatal(err)
	}

	completion := briefCompletionCommand(t, string(brief), task.ID)
	sophonBinary := buildSophonBinary(t)
	scrubbed := exec.Command("/bin/sh", "-c", completion)
	scrubbed.Dir = spawned.WorktreePath
	scrubbed.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + filepath.Dir(sophonBinary) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	output, err := scrubbed.CombinedOutput()
	if err != nil {
		t.Fatalf("worker completion in scrubbed environment failed (data-home divergence): %v\n%s", err, output)
	}
	if status := fixture.taskStatus(t, task.ID); status.State != store.StateReady {
		t.Fatalf("status after scrubbed completion = %+v, want ready", status)
	}
}

// TestCLIWorkerCompletionWakesAttachedCommander reproduces the liveness
// defect: a live commander went idle after dispatch and nothing delivered the
// worker's completion notification. After `sophon commander attach`, a
// durable result publication must best-effort wake the registered commander
// with a fixed Sophon-generated message.
func TestCLIWorkerCompletionWakesAttachedCommander(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Commander wake")
	task := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, task.ID)

	attached := runCLI(t, "commander", "attach", "--pane", "w1:p1",
		"--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	if !strings.Contains(string(attached), "w1:p1") {
		t.Fatalf("commander attach = %s", attached)
	}
	registration := readCommanderRegistration(t, fixture.home)
	if registration.Session != "fm-lab-cli-test" || registration.PaneID != "w1:p1" {
		t.Fatalf("registration = %+v", registration)
	}
	before := readLogLines(t, fixture.herdrLog)

	fixture.completeWorker(t, mission.ID, task.ID, 1)

	wake := logDelta(t, fixture.herdrLog, before)
	if !strings.Contains(wake, "prompt w1:p1 ") || !strings.Contains(wake, task.ID) ||
		!strings.Contains(wake, "ready") || !strings.Contains(wake, "sophon status") ||
		!strings.Contains(wake, "sophon verify-complete "+task.ID) {
		t.Fatalf("commander wake missing or malformed in Herdr log:\n%s", wake)
	}
	if status := fixture.taskStatus(t, task.ID); status.State != store.StateReady {
		t.Fatalf("status = %+v, want ready", status)
	}
}

// TestCLIMonitorForwardsProgressCompletionAndReportWithoutDirectDuplicates is
// the faithful local transport E2E: a real background monitor accepts worker
// requests, validates canonical evidence, and submits fixed messages to the
// exact attached commander. Accepted durable events must not also take the
// direct fallback path.
func TestCLIMonitorForwardsProgressCompletionAndReportWithoutDirectDuplicates(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Monitor forwarding")
	completed := fixture.createTask(t, mission.ID, "--title", "Completed")
	fixture.spawnTask(t, completed.ID)
	runCLI(t, "commander", "attach", "--pane", "w1:p1",
		"--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")

	binary := buildSophonBinary(t)
	start := exec.Command(binary, "monitor", "start", "--herdr", fixture.herdr)
	if output, err := start.CombinedOutput(); err != nil {
		t.Fatalf("monitor start: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		stop := exec.Command(binary, "monitor", "stop")
		if output, err := stop.CombinedOutput(); err != nil {
			t.Errorf("monitor stop: %v\n%s", err, output)
		}
	})
	status := exec.Command(binary, "monitor", "status", "--json")
	if output, err := status.CombinedOutput(); err != nil || !strings.Contains(string(output), `"running": true`) ||
		strings.Contains(string(output), "generation") {
		t.Fatalf("public monitor status = %s, %v", output, err)
	}

	before := readLogLines(t, fixture.herdrLog)
	progress := runCLI(t, "worker", "progress", completed.ID, "--attempt", "1",
		"--phase", "testing", "--message", "unit tests\nstarted")
	if !strings.Contains(string(progress), `"status": "accepted"`) {
		t.Fatalf("progress acknowledgement = %s", progress)
	}
	waitFor(t, 3*time.Second, func() bool {
		delta := logDelta(t, fixture.herdrLog, before)
		return strings.Contains(delta, completed.ID) && strings.Contains(delta, "testing phase") &&
			strings.Contains(delta, "unit tests started")
	}, "monitor-forwarded progress")

	before = readLogLines(t, fixture.herdrLog)
	fixture.completeWorker(t, mission.ID, completed.ID, 1)
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(logDelta(t, fixture.herdrLog, before), "sophon verify-complete "+completed.ID)
	}, "monitor-forwarded completion")
	completionWake := logDelta(t, fixture.herdrLog, before)
	if count := strings.Count(completionWake, "prompt w1:p1 "); count != 1 {
		t.Fatalf("accepted completion used duplicate direct fallback (%d prompts):\n%s", count, completionWake)
	}

	reported := fixture.createTask(t, mission.ID, "--title", "Reported")
	spawn := fixture.spawnTask(t, reported.ID)
	reportPath := store.AttemptPath(fixture.home, mission.ID, reported.ID, 1, store.ReportSubmissionName)
	report := store.WorkerReport{Version: 1, Status: store.WorkerReportBlocked, TaskID: reported.ID, Attempt: 1,
		HeadSHA: spawn.BaseSHA, Reason: "dependency unavailable", Verification: []domain.VerificationResult{},
		Evidence: []string{"dependency command failed"}, ChangedFiles: []string{}, DirtyWork: false, Risks: []string{}}
	if err := store.Publish(reportPath, report); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(reportPath, future, future); err != nil {
		t.Fatal(err)
	}
	before = readLogLines(t, fixture.herdrLog)
	runCLI(t, "worker", "report", reported.ID, "--attempt", "1", "--head-sha", spawn.BaseSHA,
		"--report", reportPath, "--git", fixture.git, "--herdr", fixture.herdr)
	waitFor(t, 3*time.Second, func() bool {
		delta := logDelta(t, fixture.herdrLog, before)
		return strings.Contains(delta, reported.ID) && strings.Contains(delta, "durable blocked report")
	}, "monitor-forwarded typed report")
	reportWake := logDelta(t, fixture.herdrLog, before)
	if count := strings.Count(reportWake, "prompt w1:p1 "); count != 1 {
		t.Fatalf("accepted report used duplicate direct fallback (%d prompts):\n%s", count, reportWake)
	}

	// The same JSON-RPC completion path must preserve the stronger
	// correction-ready drain trigger for a later revision of one open PR.
	continued := fixture.createTask(t, mission.ID, "--title", "Continued review", "--delivery", "pr")
	fixture.spawnTask(t, continued.ID)
	before = readLogLines(t, fixture.herdrLog)
	fixture.completeWorker(t, mission.ID, continued.ID, 1)
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(logDelta(t, fixture.herdrLog, before), "sophon verify-complete "+continued.ID)
	}, "monitor-forwarded first-revision completion")
	before = readLogLines(t, fixture.herdrLog)
	fixture.verifyComplete(t, continued.ID)
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(logDelta(t, fixture.herdrLog, before), "durable verification change")
	}, "monitor-forwarded first-revision verification")
	before = readLogLines(t, fixture.herdrLog)
	runCLI(t, "deliver", continued.ID, "--confirmed", "--git", fixture.git, "--gh-axi", fixture.ghAxi)
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(logDelta(t, fixture.herdrLog, before), "durable delivery change")
	}, "monitor-forwarded first-revision delivery")
	revisionJSON := runCLI(t, "revise", continued.ID,
		"--reason", "Accepted feedback corrects the same behavior.",
		"--objective", "Apply only the bounded correction beyond the current pull request head.",
		"--herdr", fixture.herdr, "--treehouse", fixture.treehouse, "--git", fixture.git,
		"--gh-axi", fixture.ghAxi, "--herdr-session", "fm-lab-cli-test")
	var revision store.Spawn
	if err := json.Unmarshal(revisionJSON, &revision); err != nil || revision.Revision != 2 || revision.Attempt != 2 {
		t.Fatalf("monitor correction revision = %+v, %v", revision, err)
	}
	before = readLogLines(t, fixture.herdrLog)
	fixture.completeWorker(t, mission.ID, continued.ID, 2)
	waitFor(t, 3*time.Second, func() bool {
		delta := logDelta(t, fixture.herdrLog, before)
		return strings.Contains(delta, "derives correction-ready") &&
			strings.Contains(delta, "sophon verify-complete "+continued.ID)
	}, "monitor-forwarded correction completion")
	correctionWake := logDelta(t, fixture.herdrLog, before)
	if count := strings.Count(correctionWake, "prompt w1:p1 "); count != 1 {
		t.Fatalf("accepted correction completion used duplicate direct fallback (%d prompts):\n%s", count, correctionWake)
	}
}

func TestCLIProgressWithoutMonitorIsNonfatalAndWritesNoTruth(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Missing progress monitor")
	task := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, task.ID)
	output := runCLI(t, "worker", "progress", task.ID, "--attempt", "1", "--phase", "investigating",
		"--message", "starting")
	if !strings.Contains(string(output), `"status": "unavailable"`) {
		t.Fatalf("missing-monitor progress = %s", output)
	}
	if status := fixture.taskStatus(t, task.ID); status.State == store.StateReady || status.State == store.StateAttention {
		t.Fatalf("progress created lifecycle truth: %+v", status)
	}
}

func TestCLIConcurrentMonitorStartsConvergeAndStopCleansRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	binary := buildSophonBinary(t)
	t.Cleanup(func() { _, _ = exec.Command(binary, "monitor", "stop").CombinedOutput() })
	type result struct {
		output []byte
		err    error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			command := exec.Command(binary, "monitor", "start")
			output, err := command.CombinedOutput()
			results <- result{output: output, err: err}
		}()
	}
	for i := 0; i < 2; i++ {
		started := <-results
		if started.err != nil || !strings.Contains(string(started.output), `"running": true`) {
			t.Fatalf("concurrent monitor start = %s, %v", started.output, started.err)
		}
	}
	statusCommand := exec.Command(binary, "monitor", "status", "--json")
	statusOutput, err := statusCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("monitor status: %v\n%s", err, statusOutput)
	}
	var status struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
	}
	if err := json.Unmarshal(statusOutput, &status); err != nil || !status.Running || status.PID <= 0 {
		t.Fatalf("monitor status = %s, %v", statusOutput, err)
	}
	stopCommand := exec.Command(binary, "monitor", "stop")
	if output, err := stopCommand.CombinedOutput(); err != nil {
		t.Fatalf("monitor stop: %v\n%s", err, output)
	}
	for _, path := range []string{filepath.Join(home, "state", "monitor", "runtime.json"),
		filepath.Join(home, "state", "monitor", "rpc.sock")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("monitor stop left %s: %v", path, err)
		}
	}
}

// TestCLICompletionWithoutCommanderStaysDurable proves notification failure
// modes never change durable completion: no registration, a malformed record,
// and an unreachable target all leave publication successful and status ready.
func TestCLICompletionWithoutCommanderStaysDurable(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "No commander")
	task := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, task.ID)

	// No commander attached: completion succeeds and sends nothing.
	before := readLogLines(t, fixture.herdrLog)
	fixture.completeWorker(t, mission.ID, task.ID, 1)
	if status := fixture.taskStatus(t, task.ID); status.State != store.StateReady {
		t.Fatalf("status = %+v, want ready", status)
	}
	if delta := logDelta(t, fixture.herdrLog, before); strings.Contains(delta, "prompt ") {
		t.Fatalf("unexpected commander wake without registration:\n%s", delta)
	}

	// A malformed registration is a bounded diagnostic, never a task failure.
	second := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, second.ID)
	writeCLIFile(t, commanderRegistrationPath(fixture.home), "garbage\n", 0o600)
	fixture.completeWorker(t, mission.ID, second.ID, 1)
	if status := fixture.taskStatus(t, second.ID); status.State != store.StateReady {
		t.Fatalf("status with malformed registration = %+v, want ready", status)
	}

	// A dead notification target (pane lost) is likewise diagnostic-only.
	third := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, third.ID)
	runCLI(t, "commander", "attach", "--pane", "w1:p1",
		"--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	dead := filepath.Join(filepath.Dir(fixture.herdr), "fake-herdr-dead")
	writeCLIFile(t, dead, `#!/bin/sh
set -eu
case "$1 $2" in
  "pane get") printf '{"error":{"code":"pane_not_found"}}\n' ;;
  "agent get") printf '{"error":{"code":"pane_not_found"}}\n' ;;
  *) exit 2 ;;
esac
`, 0o700)
	if _, err := runCLIErr(t, "worker", "complete", third.ID, "--attempt", "1",
		"--head-sha", commitWorkerChange(t, mission.ID, third.ID, 1),
		"--result", writeWorkerResult(t, fixture.home, mission.ID, third.ID, 1),
		"--git", fixture.git, "--herdr", dead); err != nil {
		t.Fatalf("completion with dead commander target must stay durable: %v", err)
	}
	if status := fixture.taskStatus(t, third.ID); status.State != store.StateReady {
		t.Fatalf("status with dead commander = %+v, want ready", status)
	}

	// A fresh attach replaces only the volatile address and the next
	// completion wakes the new target.
	fourth := fixture.createTask(t, mission.ID)
	fixture.spawnTask(t, fourth.ID)
	runCLI(t, "commander", "attach", "--pane", "w1:p1",
		"--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")
	before = readLogLines(t, fixture.herdrLog)
	fixture.completeWorker(t, mission.ID, fourth.ID, 1)
	if delta := logDelta(t, fixture.herdrLog, before); !strings.Contains(delta, "prompt w1:p1 ") ||
		!strings.Contains(delta, fourth.ID) {
		t.Fatalf("re-attached commander was not woken:\n%s", delta)
	}
}

// TestCLIAttachValidatesExactHerdrIdentity refuses registrations that could
// target an unrelated or nonexistent Herdr session or pane.
func TestCLIAttachValidatesExactHerdrIdentity(t *testing.T) {
	fixture := newCLIFixture(t)
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"missing pane", []string{"commander", "attach", "--herdr", fixture.herdr}, "pane"},
		{"shell metacharacters", []string{"commander", "attach", "--pane", "w1:p1;rm -rf /", "--herdr", fixture.herdr}, "syntax"},
		{"flag injection", []string{"commander", "attach", "--pane", "--session", "--herdr", fixture.herdr}, "syntax"},
		{"lost pane", []string{"commander", "attach", "--pane", "w1:p9", "--herdr", fixture.herdr}, ""},
	} {
		if _, err := runCLIErr(t, test.args...); err == nil ||
			(test.want != "" && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want substring %q", test.name, err, test.want)
		}
	}
	if _, err := os.Stat(commanderRegistrationPath(fixture.home)); !os.IsNotExist(err) {
		t.Fatal("refused attaches must not leave a registration behind")
	}
}

// commanderRegistrationPath is the volatile commander wake address the
// attach command publishes (liveness-only, never truth). Declared locally so
// these reproduction tests compile against the pre-fix tree.
func commanderRegistrationPath(home string) string {
	return filepath.Join(home, "state", "commander.json")
}

type commanderRegistration struct {
	Session string `json:"session"`
	PaneID  string `json:"pane_id"`
}

func readCommanderRegistration(t *testing.T, home string) commanderRegistration {
	t.Helper()
	data, err := os.ReadFile(commanderRegistrationPath(home))
	if err != nil {
		t.Fatalf("read commander registration: %v", err)
	}
	var registration commanderRegistration
	if err := json.Unmarshal(data, &registration); err != nil {
		t.Fatalf("decode commander registration: %v", err)
	}
	return registration
}

// briefCompletionCommand extracts the exact completion command line for the
// task from the generated brief's fenced bash block. Earlier overlay example
// lines use placeholders, so the task ID anchors the real command.
func briefCompletionCommand(t *testing.T, brief, taskID string) string {
	t.Helper()
	for _, line := range strings.Split(brief, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "sophon worker complete ") && strings.Contains(trimmed, taskID) {
			return trimmed
		}
	}
	t.Fatalf("brief contains no worker completion command for %s:\n%s", taskID, brief)
	return ""
}

// buildSophonBinary compiles the real CLI for scrubbed-environment
// subprocess runs, which must not inherit the test process environment.
func buildSophonBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "sophon")
	command := exec.Command("go", "build", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build sophon CLI: %v\n%s", err, output)
	}
	return binary
}

// commitWorkerChange simulates the worker's Git side of an attempt and
// returns the new head SHA.
func commitWorkerChange(t *testing.T, missionID, taskID string, attempt int) string {
	t.Helper()
	spawned, err := store.ReadSpawn(missionID, taskID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("change-%d.txt", attempt)
	writeCLIFile(t, filepath.Join(spawned.WorktreePath, name), "change\n", 0o600)
	runCLIGit(t, spawned.WorktreePath, "add", name)
	runCLIGit(t, spawned.WorktreePath, "commit", "-m", "change")
	return runCLIGit(t, spawned.WorktreePath, "rev-parse", "HEAD")
}

// writeWorkerResult writes a schema-valid result into the attempt directory.
func writeWorkerResult(t *testing.T, home, missionID, taskID string, attempt int) string {
	t.Helper()
	name := fmt.Sprintf("change-%d.txt", attempt)
	resultPath := store.AttemptPath(home, missionID, taskID, attempt, store.CompletionSubmissionName)
	writeCLIFile(t, resultPath, `{"version":1,"status":"completed","summary":"changed",`+
		`"verification":[{"command":"go test ./...","exit_code":0}],"changed_files":["`+name+`"],"risks":[]}`, 0o600)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(resultPath, future, future); err != nil {
		t.Fatal(err)
	}
	return resultPath
}

func readLogLines(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// logDelta returns the log content appended since the before snapshot.
func logDelta(t *testing.T, path, before string) string {
	t.Helper()
	after := readLogLines(t, path)
	if !strings.HasPrefix(after, before) {
		t.Fatalf("Herdr log shrank: before %q after %q", before, after)
	}
	return after[len(before):]
}

// TestCLIVerifiedWorkerPaneIsRetired reproduces the captain's third failure:
// a finished worker stayed as an idle pane forever. Successful verification
// of a no-validation task is terminal worker evidence and must close the
// exact task-owned tab, leaving the outcome verified and the lease in place.
func TestCLIVerifiedWorkerPaneIsRetired(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Worker retirement")
	task := fixture.createTask(t, mission.ID)
	spawned := fixture.spawnTask(t, task.ID)
	fixture.completeWorker(t, mission.ID, task.ID, 1)

	before := readLogLines(t, fixture.herdrLog)
	fixture.verifyComplete(t, task.ID)
	if delta := logDelta(t, fixture.herdrLog, before); !strings.Contains(delta, "tab-close "+spawned.Pane.TabID) {
		t.Fatalf("verified worker pane was not retired; Herdr calls:\n%s", delta)
	}
	if status := fixture.taskStatus(t, task.ID); status.State != store.StateVerified {
		t.Fatalf("status after retirement = %+v, want verified (never lost)", status)
	}
	// The lease and worktree survive pane retirement until explicit release.
	released := runCLI(t, "release", task.ID, "--treehouse", fixture.treehouse)
	if !strings.Contains(string(released), spawned.LeaseID) {
		t.Fatalf("lease did not survive worker pane retirement: %s", released)
	}
}

// TestCLIValidatedWorkerRetiresOnlyAfterPassingValidation pins the terminal
// evidence boundary for validated tasks: verification keeps the pane
// available for correction, failed validation keeps it, and only a passing
// validation retires the exact worker tab.
func TestCLIValidatedWorkerRetiresOnlyAfterPassingValidation(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Validated retirement")

	passing := fixture.createTask(t, mission.ID, "--validate", "test -f change-1.txt")
	passSpawn := fixture.spawnTask(t, passing.ID)
	fixture.completeWorker(t, mission.ID, passing.ID, 1)
	before := readLogLines(t, fixture.herdrLog)
	fixture.verifyComplete(t, passing.ID)
	if delta := logDelta(t, fixture.herdrLog, before); strings.Contains(delta, "tab-close ") {
		t.Fatalf("validated worker retired at verification; correction path lost:\n%s", delta)
	}
	before = readLogLines(t, fixture.herdrLog)
	runCLI(t, "validate", passing.ID, "--git", fixture.git, "--herdr", fixture.herdr)
	if delta := logDelta(t, fixture.herdrLog, before); !strings.Contains(delta, "tab-close "+passSpawn.Pane.TabID) {
		t.Fatalf("passing validation did not retire the worker pane:\n%s", delta)
	}

	failing := fixture.createTask(t, mission.ID, "--validate", "exit 1")
	failSpawn := fixture.spawnTask(t, failing.ID)
	fixture.completeWorker(t, mission.ID, failing.ID, 1)
	fixture.verifyComplete(t, failing.ID)
	before = readLogLines(t, fixture.herdrLog)
	if _, err := runCLIErr(t, "validate", failing.ID, "--git", fixture.git, "--herdr", fixture.herdr); err == nil {
		t.Fatal("failing validation must remain a failure")
	}
	if delta := logDelta(t, fixture.herdrLog, before); strings.Contains(delta, "tab-close "+failSpawn.Pane.TabID) {
		t.Fatalf("failed validation retired the worker pane; correction path lost:\n%s", delta)
	}
}

// TestCLISpawnGroupsWorkerTabIntoAttachedCommanderWorkspace reproduces the
// presentation defect: every worker spawned its own Herdr workspace. With an
// attached commander, workers must become task tabs inside the commander's
// exact registered workspace, and the spawn receipt must record the actual
// response-derived placement.
func TestCLISpawnGroupsWorkerTabIntoAttachedCommanderWorkspace(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Grouped worker tabs")
	runCLI(t, "commander", "attach", "--pane", "w1:p1", "--workspace", "w1", "--tab", "w1:t1",
		"--herdr", fixture.herdr, "--herdr-session", "fm-lab-cli-test")

	before := readLogLines(t, fixture.herdrLog)
	var first, second store.Spawn
	for i, title := range []string{"First", "Second"} {
		task := fixture.createTask(t, mission.ID, "--title", title)
		output := runCLI(t, fixture.spawnArgs(task.ID)...)
		spawned := &first
		if i == 1 {
			spawned = &second
		}
		if err := json.Unmarshal(output, spawned); err != nil {
			t.Fatal(err)
		}
		if spawned.Pane.WorkspaceID != "w1" || spawned.Pane.SessionName != "fm-lab-cli-test" {
			t.Fatalf("worker %s not grouped into the commander workspace: %+v", title, spawned.Pane)
		}
	}
	if first.Pane.TabID == second.Pane.TabID || first.Pane.PaneID == second.Pane.PaneID ||
		first.Pane.TabID == "w1:t1" || second.Pane.TabID == "w1:t1" {
		t.Fatalf("grouped workers must be distinct tabs, not the commander tab: %+v %+v", first.Pane, second.Pane)
	}
	delta := logDelta(t, fixture.herdrLog, before)
	if strings.Contains(delta, "workspace-create ") {
		t.Fatalf("grouped spawn created a per-worker workspace:\n%s", delta)
	}
	if count := strings.Count(delta, "tab-create workspace=w1 "); count != 2 {
		t.Fatalf("expected 2 tab-create calls in the commander workspace, got %d:\n%s", count, delta)
	}

	// Retiring one finished worker leaves the commander tab and the sibling
	// worker tab untouched.
	fixture.completeWorker(t, mission.ID, first.TaskID, 1)
	fixture.verifyComplete(t, first.TaskID)
	delta = logDelta(t, fixture.herdrLog, before)
	if !strings.Contains(delta, "tab-close "+first.Pane.TabID) {
		t.Fatalf("finished worker tab was not retired:\n%s", delta)
	}
	for _, survivor := range []string{"w1:t1", second.Pane.TabID} {
		if strings.Contains(delta, "tab-close "+survivor) {
			t.Fatalf("retirement closed unrelated tab %s:\n%s", survivor, delta)
		}
	}
}

// TestCLISpawnWithoutCommanderFallsBackToIsolatedWorkspace pins the
// documented safe fallback: no valid registration in the same explicit
// session means the worker gets its own isolated workspace, as before.
func TestCLISpawnWithoutCommanderFallsBackToIsolatedWorkspace(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Ungrouped fallback")
	task := fixture.createTask(t, mission.ID)
	spawned := fixture.spawnTask(t, task.ID)
	if spawned.Pane.PaneID != "w1:p1" {
		t.Fatalf("fallback spawn = %+v, want isolated workspace pane w1:p1", spawned.Pane)
	}
	if log := readLogLines(t, fixture.herdrLog); !strings.Contains(log, "workspace-create ") {
		t.Fatalf("fallback spawn did not create an isolated workspace:\n%s", log)
	}
}

// TestCLIStatusListsDrainActions pins status as an action queue: every ready
// task lists its exact verify-complete command, a verified task with pending
// validation lists its exact validate command after the remaining verify
// commands, and drained actions disappear. A commander reading this output
// never has to invent the next deterministic step.
func TestCLIStatusListsDrainActions(t *testing.T) {
	fixture := newCLIFixture(t)
	mission := fixture.createMission(t, "Status action queue")
	plain := fixture.createTask(t, mission.ID)
	validated := fixture.createTask(t, mission.ID, "--validate", "test -f change-1.txt")
	fixture.spawnTask(t, plain.ID)
	fixture.spawnTask(t, validated.ID)
	fixture.completeWorker(t, mission.ID, plain.ID, 1)
	fixture.completeWorker(t, mission.ID, validated.ID, 1)

	text := string(runCLI(t, "status"))
	for _, task := range []store.Task{plain, validated} {
		if !strings.Contains(text, "ACTION\tsophon verify-complete "+task.ID) {
			t.Fatalf("status omitted the verify-complete action for %s:\n%s", task.ID, text)
		}
	}
	report := fixture.statusReport(t)
	if len(report.Actions) != 2 {
		t.Fatalf("status actions = %+v, want 2 verify-complete actions", report.Actions)
	}

	// Verifying the validated task swaps its action to validate, after the
	// remaining verify action.
	fixture.verifyComplete(t, validated.ID)
	report = fixture.statusReport(t)
	if len(report.Actions) != 2 ||
		report.Actions[0] != (flow.Action{TaskID: plain.ID, Kind: flow.ActionVerifyComplete,
			Command: "sophon verify-complete " + plain.ID}) ||
		report.Actions[1] != (flow.Action{TaskID: validated.ID, Kind: flow.ActionValidate,
			Command: "sophon validate " + validated.ID}) {
		t.Fatalf("status actions after verify = %+v", report.Actions)
	}

	// A passing validation and the last verification drain the queue.
	runCLI(t, "validate", validated.ID, "--git", fixture.git, "--herdr", fixture.herdr)
	fixture.verifyComplete(t, plain.ID)
	if report = fixture.statusReport(t); len(report.Actions) != 0 {
		t.Fatalf("status actions after drain = %+v, want empty", report.Actions)
	}
	if text = string(runCLI(t, "status")); strings.Contains(text, "ACTION\t") {
		t.Fatalf("drained status still lists actions:\n%s", text)
	}
}
