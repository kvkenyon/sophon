package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/herdr"
	"sophon/internal/store"
)

// herdrLab provisions an isolated named Herdr lab session through the
// brief-mandated helper and fails closed on any unsafe name. It never touches
// the default fleet session.
func herdrLab(t *testing.T) (helper, sessionName string) {
	t.Helper()
	if os.Getenv("SOPHON_HERDR_LAB") != "1" {
		t.Skip("set SOPHON_HERDR_LAB=1 to run the real Herdr lab lifecycle proof")
	}
	helper = os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		t.Skip("set HERDR_LAB_HELPER to the fm-herdr-lab.sh path")
	}
	nameOutput, err := exec.Command(helper, "name", "sophon-worker-env-commander-wake").Output()
	if err != nil {
		t.Fatalf("derive Herdr lab session name: %v", err)
	}
	sessionName = strings.TrimSpace(string(nameOutput))
	if !strings.HasPrefix(sessionName, "fm-lab-") || sessionName == "default" {
		t.Fatalf("unsafe Herdr lab session %q", sessionName)
	}
	if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("provision Herdr lab: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command(helper, "teardown", sessionName).CombinedOutput(); err != nil {
			t.Errorf("teardown Herdr lab: %v: %s", err, output)
		}
	})
	return helper, sessionName
}

// TestLiveWorkerLifecycleInHerdrLab is the guarded real-lifecycle proof: it
// spawns a real Codex worker pane in an isolated Herdr lab session against a
// real temp Git project and the real treehouse binary. It never touches the
// default Herdr session. Run with:
//
//	SOPHON_HERDR_LAB=1 HERDR_LAB_HELPER=/path/to/fm-herdr-lab.sh go test ./cmd/sophon -run TestLiveWorkerLifecycle
func TestLiveWorkerLifecycleInHerdrLab(t *testing.T) {
	_, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
	}
	herdrBinary, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr binary not on PATH")
	}

	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv("HERDR_TAB_ID", "")
	t.Setenv("SOPHON_PROMPT_DIR", "")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is required")
	}
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, project, "init", "-b", "main")
	runCLIGit(t, project, "config", "user.name", "Sophon Lab")
	runCLIGit(t, project, "config", "user.email", "lab@example.invalid")
	writeCLIFile(t, filepath.Join(project, "base.txt"), "base\n", 0o600)
	runCLIGit(t, project, "add", "base.txt")
	runCLIGit(t, project, "commit", "-m", "base")

	fixture := &cliFixture{home: home, project: project, git: "git",
		treehouse: treehouseBinary, herdr: herdrBinary, ghAxi: "gh-axi"}
	mission := fixture.createMission(t, "Herdr lab lifecycle")
	task := fixture.createTask(t, mission.ID)

	spawnedJSON := runCLI(t, "spawn", task.ID, "--herdr", herdrBinary, "--treehouse", treehouseBinary,
		"--git", "git", "--herdr-session", sessionName)
	var spawned store.Spawn
	if err := json.Unmarshal(spawnedJSON, &spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.Pane.PaneID == "" || spawned.Pane.SessionName != sessionName {
		t.Fatalf("spawned pane = %+v", spawned.Pane)
	}
	if _, err := store.ReadSpawn(mission.ID, task.ID, 1); err != nil {
		t.Fatalf("spawn receipt: %v", err)
	}

	// The live pane is observable through a fresh status invocation.
	status := runCLI(t, "status", "--json", "--herdr", herdrBinary, "--herdr-session", sessionName)
	if !strings.Contains(string(status), task.ID) {
		t.Fatalf("status omitted the spawned task: %s", status)
	}

	// Stop the pane through the herdr CLI in the lab session.
	if output, err := exec.Command(herdrBinary, "tab", "close", spawned.Pane.TabID,
		"--session", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("stop worker pane: %v: %s", err, output)
	}
}

// TestLiveCommanderWakeGroupingAndRetirementInHerdrLab proves the full fix
// end to end against a real Herdr lab session: a live commander attaches,
// two workers group as tabs into its exact workspace with the resolved data
// home on their launch commands, a durable completion wakes the commander
// pane, and terminal verification retires exactly the finished worker's tab
// while the commander and sibling tabs survive. Run with:
//
//	SOPHON_HERDR_LAB=1 HERDR_LAB_HELPER=/path/to/fm-herdr-lab.sh go test ./cmd/sophon -run TestLiveCommanderWake -timeout 20m
func TestLiveCommanderWakeGroupingAndRetirementInHerdrLab(t *testing.T) {
	_, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
	}
	herdrBinary, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr binary not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is required")
	}

	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv("HERDR_TAB_ID", "")
	t.Setenv("SOPHON_PROMPT_DIR", "")
	ctx := context.Background()

	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, project, "init", "-b", "main")
	runCLIGit(t, project, "config", "user.name", "Sophon Lab")
	runCLIGit(t, project, "config", "user.email", "lab@example.invalid")
	writeCLIFile(t, filepath.Join(project, "base.txt"), "base\n", 0o600)
	runCLIGit(t, project, "add", "base.txt")
	runCLIGit(t, project, "commit", "-m", "base")

	fixture := &cliFixture{home: home, project: project, git: "git",
		treehouse: treehouseBinary, herdr: herdrBinary, ghAxi: "gh-axi"}
	mission := fixture.createMission(t, "Herdr lab wake and retirement")

	// A live commander pane in its own workspace, launched like any unmanaged
	// commander session.
	adapter := herdr.NewCommandAdapter(herdrBinary, sessionName, "sophon")
	commander, err := adapter.StartCodex(ctx, herdr.StartRequest{
		AgentName: "lab-commander", Attempt: 1, WorktreePath: project,
		Brief: "You are a lab commander pane. Answer briefly, then stay idle."})
	if err != nil {
		t.Fatalf("launch lab commander: %v", err)
	}
	runCLI(t, "commander", "attach", "--pane", commander.PaneID,
		"--workspace", commander.WorkspaceID, "--tab", commander.TabID,
		"--herdr", herdrBinary, "--herdr-session", sessionName)

	// Two workers must become distinct tabs inside the commander's exact
	// workspace, each carrying the resolved data home on its launch command.
	spawnTask := func(taskID string) store.Spawn {
		t.Helper()
		output := runCLI(t, "spawn", taskID, "--herdr", herdrBinary, "--treehouse", treehouseBinary,
			"--git", "git", "--herdr-session", sessionName)
		var spawned store.Spawn
		if err := json.Unmarshal(output, &spawned); err != nil {
			t.Fatal(err)
		}
		return spawned
	}
	taskA := fixture.createTask(t, mission.ID)
	spawnA := spawnTask(taskA.ID)
	taskB := fixture.createTask(t, mission.ID)
	spawnB := spawnTask(taskB.ID)
	for _, spawned := range []store.Spawn{spawnA, spawnB} {
		if spawned.Pane.WorkspaceID != commander.WorkspaceID || spawned.Pane.SessionName != sessionName {
			t.Fatalf("worker not grouped into the commander workspace: %+v", spawned.Pane)
		}
		// The pane wraps the long launch line; squash whitespace to match.
		echo := strings.Join(strings.Fields(labPaneRead(t, herdrBinary, sessionName, spawned.Pane.PaneID, 2000)), "")
		if !strings.Contains(echo, "SOPHON_DATA_HOME='"+home+"'codex--dangerously") {
			t.Fatalf("worker launch omitted the resolved data home; pane shows:\n%s", echo)
		}
	}
	if spawnA.Pane.TabID == spawnB.Pane.TabID || spawnA.Pane.TabID == commander.TabID ||
		spawnB.Pane.TabID == commander.TabID {
		t.Fatalf("tabs are not distinct: commander %s A %s B %s",
			commander.TabID, spawnA.Pane.TabID, spawnB.Pane.TabID)
	}

	// Worker A finishes: quiet its turn, clean its in-flight edits, commit,
	// and publish through the real CLI. The attached commander must be woken.
	if err := adapter.Cancel(ctx, spawnA.Pane); err != nil {
		t.Fatalf("quiet lab worker A: %v", err)
	}
	runCLIGit(t, spawnA.WorktreePath, "checkout", "--", ".")
	runCLIGit(t, spawnA.WorktreePath, "clean", "-fd")
	fixture.completeWorker(t, mission.ID, taskA.ID, 1)
	// The commander pane is a full-screen TUI; dump its content on timeout so
	// a missed wake is diagnosable from the failure message.
	wakeDeadline := time.Now().Add(90 * time.Second)
	wake := "task" + taskA.ID + "attempt1"
	for {
		pane := labPaneRead(t, herdrBinary, sessionName, commander.PaneID, 2000)
		if strings.Contains(strings.Join(strings.Fields(pane), ""), wake) {
			break
		}
		if time.Now().After(wakeDeadline) {
			t.Fatalf("commander pane never showed the completion wake; pane shows:\n%s", pane)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Terminal verification retires exactly worker A's tab; the commander tab
	// and the sibling worker tab survive.
	if status := fixture.taskStatus(t, taskA.ID); status.State != store.StateReady {
		t.Fatalf("task A status = %+v, want ready", status)
	}
	fixture.verifyComplete(t, taskA.ID)
	if status := fixture.taskStatus(t, taskA.ID); status.State != store.StateVerified {
		t.Fatalf("task A status after verification = %+v, want verified", status)
	}
	waitFor(t, 30*time.Second, func() bool {
		tabs := labTabList(t, herdrBinary, sessionName, commander.WorkspaceID)
		return tabs[commander.TabID] && tabs[spawnB.Pane.TabID] && !tabs[spawnA.Pane.TabID]
	}, "worker A tab retired, commander and sibling tabs surviving")
}

// labPaneRead reads a lab pane's content through the explicit session. The
// line window must reach past the (long) echoed brief when asserting on the
// launch command echo in scrollback.
func labPaneRead(t *testing.T, herdrBinary, sessionName, paneID string, lines int) string {
	t.Helper()
	output, err := exec.Command(herdrBinary, "pane", "read", paneID,
		"--source", "recent", "--lines", fmt.Sprint(lines), "--session", sessionName).CombinedOutput()
	if err != nil {
		t.Fatalf("read lab pane %s: %v: %s", paneID, err, output)
	}
	return string(output)
}

// labPaneVisible reads a lab pane's current viewport (no scrollback), for
// assertions about the agent's latest visible report.
func labPaneVisible(t *testing.T, herdrBinary, sessionName, paneID string) string {
	t.Helper()
	output, err := exec.Command(herdrBinary, "pane", "read", paneID,
		"--source", "visible", "--session", sessionName).CombinedOutput()
	if err != nil {
		t.Fatalf("read lab pane %s viewport: %v: %s", paneID, err, output)
	}
	return string(output)
}

// labTabList reports the tab IDs currently present in an exact lab workspace.
func labTabList(t *testing.T, herdrBinary, sessionName, workspaceID string) map[string]bool {
	t.Helper()
	output, err := exec.Command(herdrBinary, "tab", "list", "--workspace", workspaceID,
		"--session", sessionName).CombinedOutput()
	if err != nil {
		t.Fatalf("list lab tabs: %v: %s", err, output)
	}
	var response struct {
		Result struct {
			Tabs []struct {
				ID string `json:"tab_id"`
			} `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode lab tab list: %v: %s", err, output)
	}
	tabs := map[string]bool{}
	for _, tab := range response.Result.Tabs {
		tabs[tab.ID] = true
	}
	return tabs
}

// waitFor polls condition until it holds or the deadline expires.
func waitFor(t *testing.T, deadline time.Duration, condition func() bool, what string) {
	t.Helper()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-ticker.C:
		}
	}
}

// TestLiveCommanderDrainsReadyWorkInHerdrLab is the guarded behavior proof for
// the faithful failure the captain observed: a live commander listed two
// ready tasks "ready for my verification" and stopped. With the real
// commander contract loaded into a real Codex pane, an operator's "check the
// workers" must drive verification of every ready task and validation of the
// configured one before the commander responds or idles, and a completion
// wake must drive the same drain without any operator message. Run with:
//
//	SOPHON_HERDR_LAB=1 HERDR_LAB_HELPER=/path/to/fm-herdr-lab.sh go test ./cmd/sophon -run TestLiveCommanderDrains -timeout 20m
func TestLiveCommanderDrainsReadyWorkInHerdrLab(t *testing.T) {
	_, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
	}
	herdrBinary, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr binary not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is required")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain is required to build the CLI under test")
	}

	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv("HERDR_TAB_ID", "")
	t.Setenv("SOPHON_PROMPT_DIR", "")
	ctx := context.Background()

	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, project, "init", "-b", "main")
	runCLIGit(t, project, "config", "user.name", "Sophon Lab")
	runCLIGit(t, project, "config", "user.email", "lab@example.invalid")
	writeCLIFile(t, filepath.Join(project, "base.txt"), "base\n", 0o600)
	runCLIGit(t, project, "add", "base.txt")
	runCLIGit(t, project, "commit", "-m", "base")

	fixture := &cliFixture{home: home, project: project, git: "git",
		treehouse: treehouseBinary, herdr: herdrBinary, ghAxi: "gh-axi"}

	// The commander pane runs the real CLI built from this tree.
	sophonBin := filepath.Join(t.TempDir(), "sophon")
	if output, err := exec.Command("go", "build", "-o", sophonBin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build sophon for the commander pane: %v: %s", err, output)
	}

	// The real commander contract, plus the lab facts that differ from a
	// normal installation (binary not on PATH, explicit lab Herdr session).
	promptCommand := exec.Command(sophonBin, "prompt", "commander")
	promptCommand.Env = append(os.Environ(), "SOPHON_DATA_HOME="+home)
	promptBody, err := promptCommand.Output()
	if err != nil {
		t.Fatalf("compose commander prompt: %v", err)
	}
	brief := string(promptBody) + fmt.Sprintf(`
## Lab environment addendum

This session runs in an isolated Herdr lab; these facts override the generic
references above:

- The Sophon CLI is not on PATH. Invoke it by its exact path %s wherever the
  contract says to run a sophon command (for example: %s status).
- SOPHON_DATA_HOME is already set in your launch environment; never override
  or unset it.
- Append --herdr-session %s to status and send commands so pane observation
  targets this lab session. Other commands need no Herdr flags.
`, sophonBin, sophonBin, sessionName)

	adapter := herdr.NewCommandAdapter(herdrBinary, sessionName, "sophon")
	commander, err := adapter.StartCodex(ctx, herdr.StartRequest{
		AgentName: "lab-drain-commander", Attempt: 1, WorktreePath: project,
		Brief: brief, DataHome: home})
	if err != nil {
		t.Fatalf("launch lab commander: %v", err)
	}
	waitIdle := func(what string) {
		t.Helper()
		waitFor(t, 5*time.Minute, func() bool {
			state, err := adapter.Observe(ctx, commander)
			return err == nil && state == herdr.StateIdle
		}, what)
	}
	waitIdle("commander pane to settle after its contract brief")

	// The work lands only after the commander settled, matching the captain's
	// scenario.
	mission := fixture.createMission(t, "Commander drains ready work")
	taskValidated := fixture.createTask(t, mission.ID, "--validate", "test -f change-1.txt")
	taskPlain := fixture.createTask(t, mission.ID)

	attach := func() {
		t.Helper()
		runCLI(t, "commander", "attach", "--pane", commander.PaneID,
			"--workspace", commander.WorkspaceID, "--tab", commander.TabID,
			"--herdr", herdrBinary, "--herdr-session", sessionName)
	}

	spawnTask := func(taskID string) store.Spawn {
		t.Helper()
		output := runCLI(t, "spawn", taskID, "--herdr", herdrBinary, "--treehouse", treehouseBinary,
			"--git", "git", "--herdr-session", sessionName)
		var spawned store.Spawn
		if err := json.Unmarshal(output, &spawned); err != nil {
			t.Fatal(err)
		}
		return spawned
	}
	quietAndComplete := func(taskID string, spawned store.Spawn) {
		t.Helper()
		if err := adapter.Cancel(ctx, spawned.Pane); err != nil {
			t.Fatalf("quiet lab worker: %v", err)
		}
		runCLIGit(t, spawned.WorktreePath, "checkout", "--", ".")
		runCLIGit(t, spawned.WorktreePath, "clean", "-fd")
		fixture.completeWorker(t, mission.ID, taskID, 1)
	}

	// With the registration dropped, worker completions stay durable and
	// unwoken: both tasks derive ready and nothing notifies the pane.
	if err := os.Remove(store.CommanderPath(home)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop commander registration to simulate an undelivered wake: %v", err)
	}
	spawnValidated := spawnTask(taskValidated.ID)
	spawnPlain := spawnTask(taskPlain.ID)
	quietAndComplete(taskValidated.ID, spawnValidated)
	quietAndComplete(taskPlain.ID, spawnPlain)
	attach() // restores routing; attach itself sends no wake

	// Acceptance 1-3: the operator request alone must drain both ready tasks
	// (and the configured validation) before the commander reports or idles.
	if _, err := adapter.Submit(ctx, commander, "check the workers"); err != nil {
		t.Fatalf("ask the commander to check the workers: %v", err)
	}
	waitFor(t, 10*time.Minute, func() bool {
		plain := fixture.taskStatus(t, taskPlain.ID)
		validated := fixture.taskStatus(t, taskValidated.ID)
		if plain.State != store.StateVerified || validated.State != store.StateVerified {
			return false
		}
		record, err := store.ReadValidation(mission.ID, taskValidated.ID, 1)
		return err == nil && record.Passed
	}, "commander to verify both ready tasks and validate the configured one")
	waitIdle("commander pane to deliver its final report")

	// The terminal evidence retired exactly the two worker panes; the
	// commander pane survives.
	for _, spawned := range []store.Spawn{spawnValidated, spawnPlain} {
		state, err := adapter.Observe(ctx, spawned.Pane)
		if err != nil || state != herdr.StateLost {
			t.Fatalf("worker pane after drain = %s, %v; want retired (lost)", state, err)
		}
	}
	if state, err := adapter.Observe(ctx, commander); err != nil || state == herdr.StateLost {
		t.Fatalf("commander pane after drain = %s, %v; want alive", state, err)
	}

	// The final report must never contain the faithful-failure phrasing.
	// Check the visible viewport only: scrollback contains the contract echo,
	// which quotes the phrase inside its own prohibition.
	pane := labPaneVisible(t, herdrBinary, sessionName, commander.PaneID)
	if strings.Contains(strings.Join(strings.Fields(pane), ""), "readyformyverification") {
		t.Fatalf("commander reported the forbidden passive phrasing:\n%s", pane)
	}

	// Acceptance 4: a completion wake drives the same drain with no operator
	// message at all.
	taskWoken := fixture.createTask(t, mission.ID)
	spawnWoken := spawnTask(taskWoken.ID)
	quietAndComplete(taskWoken.ID, spawnWoken) // registration live: this wakes the commander
	waitFor(t, 8*time.Minute, func() bool {
		return fixture.taskStatus(t, taskWoken.ID).State == store.StateVerified
	}, "completion wake to drive verification without operator intervention")
	waitFor(t, 2*time.Minute, func() bool {
		tabs := labTabList(t, herdrBinary, sessionName, commander.WorkspaceID)
		return tabs[commander.TabID] && !tabs[spawnWoken.Pane.TabID]
	}, "woken worker tab retired, commander tab surviving")
}
