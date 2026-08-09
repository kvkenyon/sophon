package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sophon/internal/domain"
	"sophon/internal/flow"
	"sophon/internal/store"
	"sophon/internal/workspace"
)

func TestCLIWorkspaceCoordinatesTwoProjectsWithoutRootRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	root := filepath.Join(t.TempDir(), "workspace")
	runCLI(t, "workspace", "init", root)
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace root became repository: %v", err)
	}
	emptyJSON := runCLI(t, "project", "create", "empty-local", "--workspace", root, "--initial-branch", "trunk")
	var empty workspace.Project
	if err := json.Unmarshal(emptyJSON, &empty); err != nil || !empty.Unborn || empty.Branch != "trunk" {
		t.Fatalf("empty project = %+v, %v", empty, err)
	}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, source, "init", "-b", "main")
	runCLIGit(t, source, "config", "user.name", "CLI Workspace")
	runCLIGit(t, source, "config", "user.email", "cli-workspace@example.invalid")
	writeCLIFile(t, filepath.Join(source, "base.txt"), "base\n", 0o600)
	runCLIGit(t, source, "add", "base.txt")
	runCLIGit(t, source, "commit", "-m", "Initial fixture")
	runCLI(t, "project", "clone", "remote-backed", "--workspace", root, "--source", source)

	createMission := func(key string) store.Mission {
		t.Helper()
		output := runCLI(t, "mission", "create", "--workspace", root, "--project", key,
			"--title", "Coordinate "+key, "--objective", "Implement the accepted outcome in "+key)
		var mission store.Mission
		if err := json.Unmarshal(output, &mission); err != nil {
			t.Fatal(err)
		}
		return mission
	}
	emptyMission := createMission("empty-local")
	remoteMission := createMission("remote-backed")
	if emptyMission.WorkspaceID == "" || emptyMission.ProjectKey != "empty-local" ||
		remoteMission.WorkspaceID != emptyMission.WorkspaceID || remoteMission.ProjectKey != "remote-backed" {
		t.Fatalf("workspace mission identities: empty=%+v remote=%+v", emptyMission, remoteMission)
	}
	createLocalTask := func(mission store.Mission, title string) store.Task {
		t.Helper()
		output := runCLI(t, "task", "create", "--mission", mission.ID, "--title", title,
			"--objective", "Implement and validate the project-confined result", "--delivery", "local")
		var task store.Task
		if err := json.Unmarshal(output, &task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	emptyTask := createLocalTask(emptyMission, "Build empty project")
	remoteTask := createLocalTask(remoteMission, "Inspect remote project")
	statusJSON := runCLI(t, "status", "--json")
	var report flow.Report
	if err := json.Unmarshal(statusJSON, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Missions) != 2 || len(report.Actions) != 2 {
		t.Fatalf("workspace status = %+v", report)
	}
	seen := map[string]bool{}
	for _, mission := range report.Missions {
		seen[mission.Mission.ProjectKey] = true
		if len(mission.Tasks) != 1 || mission.Tasks[0].State != store.StatePlanned {
			t.Fatalf("project status = %+v", mission)
		}
	}
	if !seen["empty-local"] || !seen["remote-backed"] || report.Actions[0].Kind != flow.ActionStart || report.Actions[1].Kind != flow.ActionStart {
		t.Fatalf("independent project actions = %+v", report.Actions)
	}
	if emptyTask.DeliveryMode != domain.DeliveryLocal || remoteTask.DeliveryMode != domain.DeliveryLocal {
		t.Fatal("workspace tasks did not retain local posture")
	}
}

func TestCLIProjectGitHubPublicationRequiresExactSeparateAuthority(t *testing.T) {
	t.Setenv("SOPHON_DATA_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "workspace")
	runCLI(t, "workspace", "init", root)
	projectJSON := runCLI(t, "project", "create", "local", "--workspace", root)
	var project workspace.Project
	if err := json.Unmarshal(projectJSON, &project); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "gh-calls")
	gh := filepath.Join(t.TempDir(), "fake-gh-axi")
	writeCLIFile(t, gh, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+logPath+"'\n", 0o700)
	args := []string{"project", "publish", "local", "--workspace", root, "--repository", "acme/local",
		"--remote-url", "https://example.invalid/acme/local.git", "--visibility", "private", "--gh-axi", gh}
	if _, err := runCLIErr(t, args...); err == nil {
		t.Fatal("GitHub publication without confirmation succeeded")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed publication called gh-axi: %v", err)
	}
	runCLI(t, append(args, "--confirmed")...)
	data, err := os.ReadFile(logPath)
	if err != nil || strings.TrimSpace(string(data)) != "repo create acme/local --private" {
		t.Fatalf("gh-axi calls = %q, %v", data, err)
	}
	if remote := strings.TrimSpace(runCLIGit(t, project.Path, "remote", "get-url", "origin")); remote != "https://example.invalid/acme/local.git" {
		t.Fatalf("origin = %q", remote)
	}
}
