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
func herdrLab(t *testing.T) (herdrBinary, sessionName string) {
	t.Helper()
	if os.Getenv("SOPHON_HERDR_LAB") != "1" {
		t.Skip("set SOPHON_HERDR_LAB=1 to run the real Herdr lab lifecycle proof")
	}
	helper := os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		t.Skip("set HERDR_LAB_HELPER to the fm-herdr-lab.sh path")
	}
	if preprovisioned := strings.TrimSpace(os.Getenv("SOPHON_HERDR_LAB_SESSION")); preprovisioned != "" {
		if !strings.HasPrefix(preprovisioned, "fm-lab-") || preprovisioned == "default" {
			t.Fatalf("unsafe preprovisioned Herdr lab session %q", preprovisioned)
		}
		return labHerdrBinary(t, helper, preprovisioned), preprovisioned
	}
	nameOutput, err := exec.Command(helper, "name", "sophon-existing-pr-revisions").Output()
	if err != nil {
		t.Fatalf("derive Herdr lab session name: %v", err)
	}
	sessionName = strings.TrimSpace(string(nameOutput))
	if !strings.HasPrefix(sessionName, "fm-lab-") || sessionName == "default" {
		t.Fatalf("unsafe Herdr lab session %q", sessionName)
	}
	t.Cleanup(func() {
		if output, err := exec.Command(helper, "teardown", sessionName).CombinedOutput(); err != nil {
			t.Errorf("teardown Herdr lab: %v: %s", err, output)
		}
	})
	if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("provision Herdr lab: %v: %s", err, output)
	}
	return labHerdrBinary(t, helper, sessionName), sessionName
}

// labHerdrBinary makes every test-specific Herdr invocation pass through the
// isolation helper. Internal/herdr already appends an explicit --session;
// the wrapper removes that pair before the helper appends the required final
// session argument itself.
func labHerdrBinary(t *testing.T, helper, sessionName string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdr-lab")
	script := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
args=("$@")
n=${#args[@]}
if (( n < 3 )) || [[ "${args[n-2]}" != "--session" ]] || [[ "${args[n-1]}" != %s ]]; then
  echo "lab wrapper requires exact trailing --session" >&2
  exit 97
fi
exec %s run %s "${args[@]:0:n-2}"
`, shellQuote(sessionName), shellQuote(helper), shellQuote(sessionName))
	writeCLIFile(t, path, script, 0o700)
	return path
}

// TestLiveWorkspaceCommanderLocalBootstrapInHerdrLab proves the primary
// workspace boundary with real commander and worker agents. One commander
// starts at a non-Git workspace root, proposes without effects, deliberately
// exposes truthful planned state, starts empty-project work, changes project
// context, and supervises an independent remote-backed task without restart or
// reattach. Every Herdr operation goes through herdrLab's generated helper
// wrapper and exact non-default session.
func TestLiveWorkspaceCommanderLocalBootstrapInHerdrLab(t *testing.T) {
	herdrBinary, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
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

	root := filepath.Join(t.TempDir(), "workspace")
	runCLI(t, "workspace", "init", root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve canonical workspace root: %v", err)
	}
	runCLI(t, "project", "create", "empty-local", "--workspace", root, "--initial-branch", "trunk")
	source := filepath.Join(t.TempDir(), "remote-source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, source, "init", "-b", "main")
	runCLIGit(t, source, "config", "user.name", "Sophon Workspace Lab")
	runCLIGit(t, source, "config", "user.email", "workspace-lab@example.invalid")
	writeCLIFile(t, filepath.Join(source, "base.txt"), "remote fixture\n", 0o600)
	runCLIGit(t, source, "add", "base.txt")
	runCLIGit(t, source, "commit", "-m", "Initial remote fixture")
	runCLI(t, "project", "clone", "remote-backed", "--workspace", root, "--source", source)
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace root became a Git repository: %v", err)
	}

	sophonBin := buildSophonBinary(t)
	prompt := exec.Command(sophonBin, "prompt", "commander")
	prompt.Env = append(os.Environ(), "SOPHON_DATA_HOME="+home)
	promptBody, err := prompt.Output()
	if err != nil {
		t.Fatalf("compose workspace commander prompt: %v", err)
	}
	brief := string(promptBody) + fmt.Sprintf(`
## Guarded workspace lab facts

These exact lab facts override generic placeholders and command examples:

- Your workspace root is %s. Start and remain there; it is intentionally not a
  Git repository. Inspect it and list both projects before greeting.
- Sophon is not taken from PATH. Replace every `+"`sophon`"+` command with the exact
  binary %s.
- Every Sophon command that accepts Herdr flags must use --herdr %s and
  --herdr-session %s. Every start action must also use --treehouse %s --git git.
  Never invoke another Herdr binary or session.
- Attach exactly once with --scope %s. Do not reattach when project context
  changes. Keep each project outcome in its own mission, task, and worker.
- For this proof, when the operator first authorizes the empty-project build
  and explicitly asks you to pause at planned state, create the local task but
  do not run its start action until the next operator instruction.
- Give every implementation task an executable validation command. Never
  create a repository, add/change a remote, select delivery, push, or open a PR.
`, root, sophonBin, herdrBinary, sessionName, treehouseBinary, root)

	adapter := herdr.NewCommandAdapter(herdrBinary, sessionName, "sophon")
	commander, err := adapter.StartCodex(ctx, herdr.StartRequest{
		AgentName: "workspace-root-commander", Attempt: 1, WorktreePath: root,
		Brief: brief, DataHome: home})
	if err != nil {
		t.Fatalf("start workspace commander: %v", err)
	}
	t.Cleanup(func() {
		stopMonitor := exec.Command(sophonBin, "monitor", "stop")
		stopMonitor.Env = append(os.Environ(), "SOPHON_DATA_HOME="+home)
		if output, stopErr := stopMonitor.CombinedOutput(); stopErr != nil {
			t.Errorf("stop workspace lab monitor: %v: %s", stopErr, output)
		}
		if output, closeErr := exec.Command(herdrBinary, "tab", "close", commander.TabID,
			"--session", sessionName).CombinedOutput(); closeErr != nil && !strings.Contains(string(output), "not_found") {
			t.Errorf("close workspace commander tab: %v: %s", closeErr, output)
		}
	})
	waitIdle := func(what string) {
		t.Helper()
		waitFor(t, 6*time.Minute, func() bool {
			state, observeErr := adapter.Observe(ctx, commander)
			return observeErr == nil && state == herdr.StateIdle
		}, what)
	}
	waitIdle("workspace commander startup")
	// Ensure the optional transport uses this exact helper-backed Herdr adapter;
	// start is idempotent if the commander already performed its startup step.
	monitorStart := exec.Command(sophonBin, "monitor", "start", "--herdr", herdrBinary)
	monitorStart.Env = append(os.Environ(), "SOPHON_DATA_HOME="+home)
	if output, err := monitorStart.CombinedOutput(); err != nil {
		t.Fatalf("start workspace lab monitor: %v: %s", err, output)
	}
	registration, err := store.ReadCommander()
	if err != nil || registration.PaneID != commander.PaneID || registration.ScopeRoot != root || registration.ScopeWorkspaceID == "" {
		t.Fatalf("workspace commander registration = %+v, %v", registration, err)
	}
	originalAttach := registration.AttachedAt

	// Proposal language is discussion only: no mission, task, worker tab, Git
	// baseline, or durable change notification is permitted.
	if _, err := adapter.Submit(ctx, commander,
		"Decide what to build in empty-local. Propose a small dependency-free habit tracker with tests, but only talk it through."); err != nil {
		t.Fatalf("submit proposal-only request: %v", err)
	}
	waitIdle("proposal-only response")
	if missions, err := store.ListMissions(); err != nil || len(missions) != 0 {
		t.Fatalf("proposal created durable work: %+v, %v", missions, err)
	}
	emptyPath := filepath.Join(root, "projects", "empty-local")
	if _, err := exec.Command("git", "-C", emptyPath, "rev-parse", "--verify", "HEAD").CombinedOutput(); err == nil {
		t.Fatal("proposal-only request created a Git baseline")
	}
	if tabs := labTabList(t, herdrBinary, sessionName, commander.WorkspaceID); len(tabs) != 1 || !tabs[commander.TabID] {
		t.Fatalf("proposal-only request allocated a worker tab: %+v", tabs)
	}

	findProjectTask := func(key string) (store.Mission, store.Task, bool) {
		missions, listErr := store.ListMissions()
		if listErr != nil {
			return store.Mission{}, store.Task{}, false
		}
		for _, mission := range missions {
			if mission.ProjectKey != key {
				continue
			}
			tasks, taskErr := store.ListTasks(mission.ID)
			if taskErr == nil && len(tasks) > 0 {
				return mission, tasks[len(tasks)-1], true
			}
		}
		return store.Mission{}, store.Task{}, false
	}

	if _, err := adapter.Submit(ctx, commander,
		"Build that accepted habit-tracker proposal locally in empty-local. Create its project-confined local mission and task with a real test command, then pause before start so I can observe planned state."); err != nil {
		t.Fatalf("authorize planned local work: %v", err)
	}
	waitIdle("commander to publish the deliberately paused plan")
	emptyMission, emptyTask, ok := findProjectTask("empty-local")
	if !ok {
		t.Fatal("authorized build did not create the empty-local task")
	}
	planned, err := store.Derive(emptyTask)
	if err != nil || planned.State != store.StatePlanned {
		t.Fatalf("unstarted task did not derive planned: %+v, %v", planned, err)
	}
	if _, err := store.ReadSpawn(emptyMission.ID, emptyTask.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("planned task invented a spawn receipt: %v", err)
	}

	if _, err := adapter.Submit(ctx, commander, "Start development now for that exact planned empty-local task."); err != nil {
		t.Fatalf("authorize empty-local start: %v", err)
	}
	var emptySpawn store.Spawn
	waitFor(t, 6*time.Minute, func() bool {
		spawn, readErr := store.ReadSpawn(emptyMission.ID, emptyTask.ID, 1)
		if readErr == nil {
			emptySpawn = spawn
			return true
		}
		return false
	}, "empty-local bootstrap and worker allocation")
	if emptySpawn.Pane.WorkspaceID != commander.WorkspaceID || emptySpawn.Pane.SessionName != sessionName ||
		emptySpawn.WorktreePath == root || emptySpawn.WorktreePath == emptyPath {
		t.Fatalf("empty worker placement = %+v", emptySpawn)
	}
	intent, intentErr := store.ReadBootstrapIntent(emptyMission.ID, emptyTask.ID)
	receipt, receiptErr := store.ReadBootstrapReceipt(emptyMission.ID, emptyTask.ID)
	if intentErr != nil || receiptErr != nil || receipt.CommitSHA == "" ||
		receipt.CommitSHA != strings.TrimSpace(runCLIGit(t, emptyPath, "rev-parse", "HEAD")) ||
		strings.TrimSpace(runCLIGit(t, emptyPath, "ls-tree", "--name-only", "HEAD")) != "" ||
		strings.TrimSpace(runCLIGit(t, emptyPath, "remote")) != "" {
		t.Fatalf("empty bootstrap intent=%+v/%v receipt=%+v/%v", intent, intentErr, receipt, receiptErr)
	}
	// Make the source-tree binary explicit to the real worker; the worktree
	// brief remains the authority, this only avoids an unrelated installed CLI.
	runCLI(t, "send", emptyTask.ID, "Use "+sophonBin+" for every Sophon progress/completion command in your generated brief; continue the exact accepted task.",
		"--herdr", herdrBinary, "--herdr-session", sessionName)
	runCLI(t, "worker", "progress", emptyTask.ID, "--attempt", "1", "--phase", "implementing", "--message", "workspace empty project started")

	// Switch project context while the first worker remains owned by the same
	// commander. Inspection is read-only and must not reattach or infer CWD.
	waitIdle("commander after empty-local worker start")
	if _, err := adapter.Submit(ctx, commander,
		"Switch context to remote-backed and report that project's current Sophon status only. Do not create work or reattach."); err != nil {
		t.Fatalf("request second-project inspection: %v", err)
	}
	waitIdle("remote-backed status inspection")
	// The response is five blocks (the project context plus the four-section
	// status contract), so the first line can scroll one line beyond the current
	// viewport. Read real recent transcript rather than a clipped screen. The
	// operator prompt deliberately does not contain the expected answer text;
	// only the commander's response can satisfy this assertion.
	transcript := labPaneRead(t, herdrBinary, sessionName, commander.PaneID, 2000)
	if squashed := strings.Join(strings.Fields(transcript), ""); !strings.Contains(squashed, "Projectremote-backed:") {
		t.Fatalf("commander did not identify inspected project in its live turn transcript:\n%s", transcript)
	}
	registration, err = store.ReadCommander()
	if err != nil || registration.PaneID != commander.PaneID || !registration.AttachedAt.Equal(originalAttach) {
		t.Fatalf("project switch restarted or reattached commander: %+v, %v", registration, err)
	}

	if _, err := adapter.Submit(ctx, commander,
		"Start an independent local implementation in remote-backed now: add remote-marker.txt documenting workspace coordination, validate it with test -f remote-marker.txt, and keep it project-confined. Do not publish anything."); err != nil {
		t.Fatalf("authorize remote-backed work: %v", err)
	}
	var remoteMission store.Mission
	var remoteTask store.Task
	var remoteSpawn store.Spawn
	waitFor(t, 6*time.Minute, func() bool {
		var found bool
		remoteMission, remoteTask, found = findProjectTask("remote-backed")
		if !found {
			return false
		}
		spawn, readErr := store.ReadSpawn(remoteMission.ID, remoteTask.ID, 1)
		if readErr == nil {
			remoteSpawn = spawn
			return true
		}
		return false
	}, "independent remote-backed worker allocation")
	if remoteSpawn.Pane.WorkspaceID != commander.WorkspaceID || remoteSpawn.Pane.TabID == emptySpawn.Pane.TabID {
		t.Fatalf("remote-backed worker placement = %+v; empty=%+v", remoteSpawn, emptySpawn)
	}
	runCLI(t, "send", remoteTask.ID, "Use "+sophonBin+" for every Sophon progress/completion command in your generated brief; continue the exact accepted task.",
		"--herdr", herdrBinary, "--herdr-session", sessionName)
	runCLI(t, "worker", "progress", remoteTask.ID, "--attempt", "1", "--phase", "implementing", "--message", "workspace remote project started")

	// Each real worker publishes completion; independent notifications wake the
	// same commander, which drains verification and validation for both.
	waitFor(t, 18*time.Minute, func() bool {
		for _, pair := range []struct{ mission, task string }{{emptyMission.ID, emptyTask.ID}, {remoteMission.ID, remoteTask.ID}} {
			current, findErr := store.FindTask(pair.task)
			if findErr != nil {
				return false
			}
			status, deriveErr := store.Derive(current)
			validation, validationErr := store.ReadValidation(pair.mission, pair.task, 1)
			if deriveErr != nil || status.State != store.StateVerified || validationErr != nil || !validation.Passed {
				return false
			}
		}
		return true
	}, "both project workers to complete and the one commander to verify and validate them")
	registration, err = store.ReadCommander()
	if err != nil || registration.PaneID != commander.PaneID || !registration.AttachedAt.Equal(originalAttach) {
		t.Fatalf("multi-project completion replaced commander: %+v, %v", registration, err)
	}
	if state, err := adapter.Observe(ctx, commander); err != nil || state == herdr.StateLost {
		t.Fatalf("workspace commander after both notifications = %s, %v", state, err)
	}
	for _, task := range []store.Task{emptyTask, remoteTask} {
		if _, err := store.ReadDeliverySelection(task.MissionID, task.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("local task %s gained delivery selection: %v", task.ID, err)
		}
		if _, err := store.ReadDelivery(task.MissionID, task.ID, 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("local task %s gained delivery evidence: %v", task.ID, err)
		}
		runCLI(t, "release", task.ID, "--treehouse", treehouseBinary)
	}
}

// TestLiveWorkerLifecycleInHerdrLab is the guarded real-lifecycle proof: it
// spawns a real Codex worker pane in an isolated Herdr lab session against a
// real temp Git project and the real treehouse binary. It never touches the
// default Herdr session. Run with:
//
//	SOPHON_HERDR_LAB=1 HERDR_LAB_HELPER=/path/to/fm-herdr-lab.sh go test ./cmd/sophon -run TestLiveWorkerLifecycle
func TestLiveWorkerLifecycleInHerdrLab(t *testing.T) {
	herdrBinary, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
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

	// Stop the pane through the helper-routed Herdr wrapper in the lab session.
	if output, err := exec.Command(herdrBinary, "tab", "close", spawned.Pane.TabID,
		"--session", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("stop worker pane: %v: %s", err, output)
	}
}

// TestLiveCorrectionRevisionPlacementInHerdrLab proves that the correction
// launch path uses the exact already-public PR head in a real named Herdr
// session, then publishes ordinary worker completion and retires that exact
// correction tab. Git and forge effects are guarded local fakes only.
func TestLiveCorrectionRevisionPlacementInHerdrLab(t *testing.T) {
	herdrBinary, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
	}
	fixture := newCLIFixture(t)
	fixture.herdr = herdrBinary
	fixture.treehouse = treehouseBinary
	adapter := herdr.NewCommandAdapter(herdrBinary, sessionName, "sophon")
	ctx := context.Background()

	mission := fixture.createMission(t, "Exact open PR correction placement")
	task := fixture.createTask(t, mission.ID, "--delivery", "pr")
	spawnJSON := runCLI(t, "spawn", task.ID, "--herdr", herdrBinary, "--treehouse", treehouseBinary,
		"--git", fixture.git, "--herdr-session", sessionName)
	var firstSpawn store.Spawn
	if err := json.Unmarshal(spawnJSON, &firstSpawn); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cancel(ctx, firstSpawn.Pane); err != nil {
		t.Fatalf("quiet first worker: %v", err)
	}
	runCLIGit(t, firstSpawn.WorktreePath, "checkout", "--", ".")
	runCLIGit(t, firstSpawn.WorktreePath, "clean", "-fd")
	firstHead := fixture.completeWorker(t, mission.ID, task.ID, 1)
	fixture.verifyComplete(t, task.ID)
	waitFor(t, 30*time.Second, func() bool {
		state, observeErr := adapter.Observe(ctx, firstSpawn.Pane)
		return observeErr == nil && state == herdr.StateLost
	}, "first worker tab retirement")
	runCLI(t, "deliver", task.ID, "--confirmed", "--git", fixture.git, "--gh-axi", fixture.ghAxi)

	correctionJSON := runCLI(t, "revise", task.ID,
		"--reason", "Accepted feedback corrects the same behavior.",
		"--objective", "Apply only the bounded correction beyond the current PR head.",
		"--herdr", herdrBinary, "--treehouse", treehouseBinary, "--git", fixture.git,
		"--gh-axi", fixture.ghAxi, "--herdr-session", sessionName)
	var correctionSpawn store.Spawn
	if err := json.Unmarshal(correctionJSON, &correctionSpawn); err != nil {
		t.Fatal(err)
	}
	if correctionSpawn.Revision != 2 || correctionSpawn.Attempt != 2 ||
		!strings.EqualFold(correctionSpawn.BaseSHA, firstHead) ||
		!strings.EqualFold(runCLIGit(t, correctionSpawn.WorktreePath, "rev-parse", "HEAD"), firstHead) {
		t.Fatalf("live correction placement = %+v, exact PR head %s", correctionSpawn, firstHead)
	}
	if correctionSpawn.Pane.SessionName != sessionName || correctionSpawn.Pane.TabID == firstSpawn.Pane.TabID {
		t.Fatalf("live correction pane = %+v, first = %+v", correctionSpawn.Pane, firstSpawn.Pane)
	}
	if err := adapter.Cancel(ctx, correctionSpawn.Pane); err != nil {
		t.Fatalf("quiet correction worker: %v", err)
	}
	runCLIGit(t, correctionSpawn.WorktreePath, "checkout", "--", ".")
	runCLIGit(t, correctionSpawn.WorktreePath, "clean", "-fd")
	fixture.completeWorker(t, mission.ID, task.ID, 2)
	fixture.verifyComplete(t, task.ID)
	waitFor(t, 30*time.Second, func() bool {
		state, observeErr := adapter.Observe(ctx, correctionSpawn.Pane)
		return observeErr == nil && state == herdr.StateLost
	}, "correction worker tab retirement")
	runCLI(t, "release", task.ID, "--attempt", "1", "--treehouse", treehouseBinary)
	runCLI(t, "release", task.ID, "--attempt", "2", "--treehouse", treehouseBinary)
}

// TestLiveReviewCorrectionRevisionPlacementInHerdrLab proves that classified
// browser feedback uses the same landed revision/spawn owner as other
// corrections, while starting from the exact undelivered reviewed head. The
// durable product event is constructed through the public Sophon store API;
// packed-product ingestion and browser submission are proved separately.
func TestLiveReviewCorrectionRevisionPlacementInHerdrLab(t *testing.T) {
	herdrBinary, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
	}
	fixture := newCLIFixture(t)
	fixture.herdr = herdrBinary
	fixture.treehouse = treehouseBinary
	adapter := herdr.NewCommandAdapter(herdrBinary, sessionName, "sophon")
	ctx := context.Background()

	mission := fixture.createMission(t, "Exact reviewed-head correction placement")
	task := fixture.createTask(t, mission.ID, "--review", "required")
	spawnJSON := runCLI(t, "spawn", task.ID, "--herdr", herdrBinary, "--treehouse", treehouseBinary,
		"--git", fixture.git, "--herdr-session", sessionName)
	var firstSpawn store.Spawn
	if err := json.Unmarshal(spawnJSON, &firstSpawn); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cancel(ctx, firstSpawn.Pane); err != nil {
		t.Fatalf("quiet first review worker: %v", err)
	}
	runCLIGit(t, firstSpawn.WorktreePath, "checkout", "--", ".")
	runCLIGit(t, firstSpawn.WorktreePath, "clean", "-fd")
	firstHead := fixture.completeWorker(t, mission.ID, task.ID, 1)
	fixture.verifyComplete(t, task.ID)

	current, err := store.ReadTask(mission.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := store.ReviewBinding{Version: store.ReviewRecordVersion, Product: store.ReviewProduct,
		ProductSchemaVersion: store.ReviewProductSchema, TaskID: task.ID, Attempt: 1,
		SessionID: "77d91f3ddc544f34e70c1158", BaseSHA: firstSpawn.BaseSHA, HeadSHA: firstHead,
		OpenedAt: time.Now().UTC()}
	if err := store.PublishReviewBindingForTask(current, binding); err != nil {
		t.Fatal(err)
	}
	event := store.ReviewEvent{Version: store.ReviewRecordVersion, ProductSchema: store.ReviewProductSchema,
		TaskID: task.ID, Attempt: 1, SessionID: binding.SessionID, Sequence: 1,
		ProductEventID: "11111111-1111-4111-8111-111111111111", Type: "feedback",
		CreatedAt: time.Now().UTC(), BaseSHA: binding.BaseSHA, HeadSHA: binding.HeadSHA,
		Comments: []store.ReviewComment{{ID: "22222222-2222-4222-8222-222222222222", Scope: "general",
			Body: "untrusted live browser correction body", CreatedAt: time.Now().UTC()}}}
	if err := store.PublishReviewEvent(current, binding, event); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "review", "classify", task.ID, "--sequence", "1", "--disposition", "requested-changes", "--json")
	applyJSON := runCLI(t, "review", "apply", task.ID, "--sequence", "1", "--json",
		"--herdr", herdrBinary, "--treehouse", treehouseBinary, "--git", fixture.git,
		"--gh-axi", fixture.ghAxi, "--herdr-session", sessionName)
	var route store.ReviewRoute
	if err := json.Unmarshal(applyJSON, &route); err != nil {
		t.Fatal(err)
	}
	correctionSpawn, err := store.ReadSpawn(mission.ID, task.ID, route.TargetAttempt)
	if err != nil || correctionSpawn.Revision != 2 || correctionSpawn.Attempt != 2 ||
		!strings.EqualFold(correctionSpawn.BaseSHA, firstHead) || correctionSpawn.Pane.SessionName != sessionName {
		t.Fatalf("live reviewed-head correction placement = %+v route=%+v err=%v", correctionSpawn, route, err)
	}
	correction, err := store.ReadCorrection(mission.ID, task.ID, 2)
	if err != nil || store.CorrectionSource(correction) != store.CorrectionSourceReadCode ||
		correction.ReviewAttempt != 1 || len(correction.ReviewFeedback) != 1 || correction.ReviewFeedback[0] != 1 {
		t.Fatalf("live review correction intent = %+v, %v", correction, err)
	}
	brief, err := os.ReadFile(store.AttemptPath(fixture.home, mission.ID, task.ID, 2, "brief.md"))
	if err != nil || strings.Contains(string(brief), event.Comments[0].Body) ||
		!strings.Contains(string(brief), "review feedback "+task.ID+" --attempt 1") {
		t.Fatalf("live review correction brief leaked body or lost pointer: %v\n%s", err, brief)
	}
	if err := adapter.Cancel(ctx, correctionSpawn.Pane); err != nil {
		t.Fatalf("quiet review correction worker: %v", err)
	}
	runCLIGit(t, correctionSpawn.WorktreePath, "checkout", "--", ".")
	runCLIGit(t, correctionSpawn.WorktreePath, "clean", "-fd")
	fixture.completeWorker(t, mission.ID, task.ID, 2)
	fixture.verifyComplete(t, task.ID)
	runCLI(t, "release", task.ID, "--attempt", "1", "--treehouse", treehouseBinary)
	runCLI(t, "release", task.ID, "--attempt", "2", "--treehouse", treehouseBinary)
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
	herdrBinary, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
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
	// The live proof uses the real detached monitor process; its Herdr boundary
	// is still the helper-backed wrapper above.
	sophonBinary := buildSophonBinary(t)
	startMonitor := exec.Command(sophonBinary, "monitor", "start", "--herdr", herdrBinary)
	if output, err := startMonitor.CombinedOutput(); err != nil {
		t.Fatalf("start lab notification monitor: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command(sophonBinary, "monitor", "stop").CombinedOutput(); err != nil {
			t.Errorf("stop lab notification monitor: %v: %s", err, output)
		}
	})

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

	// Put the commander into a live turn, then publish worker B's completion.
	// Herdr's supported running-safe submit path must accept the monitor's
	// fixed message without screen scraping or waiting for idle.
	if _, err := adapter.Submit(ctx, commander,
		"Review the current Sophon status carefully and explain the action queue before ending this turn."); err != nil {
		t.Fatalf("start running-commander proof turn: %v", err)
	}
	if _, state, err := adapter.Identify(ctx, commander); err != nil || state != herdr.StateRunning {
		t.Fatalf("commander is not running before monitor publication: state=%s err=%v", state, err)
	}
	if err := adapter.Cancel(ctx, spawnB.Pane); err != nil {
		t.Fatalf("quiet lab worker B: %v", err)
	}
	runCLIGit(t, spawnB.WorktreePath, "checkout", "--", ".")
	runCLIGit(t, spawnB.WorktreePath, "clean", "-fd")
	fixture.completeWorker(t, mission.ID, taskB.ID, 1)
	runningWakeDeadline := time.Now().Add(90 * time.Second)
	runningWake := "task" + taskB.ID + "attempt1"
	for {
		pane := labPaneRead(t, herdrBinary, sessionName, commander.PaneID, 2000)
		if strings.Contains(strings.Join(strings.Fields(pane), ""), runningWake) {
			break
		}
		if time.Now().After(runningWakeDeadline) {
			t.Fatalf("running commander never received queued monitor wake; pane shows:\n%s", pane)
		}
		time.Sleep(500 * time.Millisecond)
	}
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
	herdrBinary, sessionName := herdrLab(t)
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
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
