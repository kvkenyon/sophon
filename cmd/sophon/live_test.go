package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sophon/internal/store"
)

// TestLiveWorkerLifecycleInHerdrLab is the guarded real-lifecycle proof: it
// spawns a real Codex worker pane in an isolated Herdr lab session against a
// real temp Git project and the real treehouse binary. It never touches the
// default Herdr session. Run with:
//
//	SOPHON_HERDR_LAB=1 HERDR_LAB_HELPER=/path/to/fm-herdr-lab.sh go test ./cmd/sophon -run TestLiveWorkerLifecycle
func TestLiveWorkerLifecycleInHerdrLab(t *testing.T) {
	if os.Getenv("SOPHON_HERDR_LAB") != "1" {
		t.Skip("set SOPHON_HERDR_LAB=1 to run the real Herdr lab lifecycle proof")
	}
	helper := os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		t.Skip("set HERDR_LAB_HELPER to the fm-herdr-lab.sh path")
	}
	treehouseBinary, err := exec.LookPath("treehouse")
	if err != nil {
		t.Skip("treehouse binary not on PATH")
	}
	herdrBinary, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr binary not on PATH")
	}
	nameOutput, err := exec.Command(helper, "name", "sophon-filesystem-prototype").Output()
	if err != nil {
		t.Fatalf("derive Herdr lab session name: %v", err)
	}
	sessionName := strings.TrimSpace(string(nameOutput))
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

	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	t.Setenv("HERDR_SESSION", "")
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
