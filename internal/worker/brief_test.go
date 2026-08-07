package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sophon/internal/domain"
	runtimeprompts "sophon/prompts"
)

func TestBriefGenerationComposesWorkerPromptsAndTaskFacts(t *testing.T) {
	t.Setenv("SOPHON_PROMPT_DIR", "")
	t.Setenv(runtimeprompts.LegacyOverrideEnv, "")
	outside := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	generator := BriefGenerator{BaseDir: filepath.Join(t.TempDir(), "tasks")}
	task := domain.Task{
		ID: "tsk_brief", MissionID: "msn_brief", Title: "Implement the slice",
		Objective: "Build the deterministic worker path.", Kind: domain.TaskImplementation,
		AcceptanceCriteria: []domain.Criterion{{Description: "Completion is independently verified."}},
		DeliveryMode:       domain.DeliveryBranch,
	}
	path, err := generator.Render(BriefInput{
		MissionID: "msn_brief", MissionTitle: "Milestone 3", MissionObjective: "Reach ready safely.",
		Task: task, Attempt: 3, Project: "sophon", Worktree: "/worktrees/m3",
		Branch: "task/m3", BaseSHA: "0123456789abcdef0123456789abcdef01234567",
		ValidationRequirements: []string{"go test ./...", "go build ./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(generator.BaseDir, "tsk_brief", "3", "brief.md")
	if path != wantPath {
		t.Fatalf("brief path = %q, want %q", path, wantPath)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"# Common worker prompt", "# Implementation worker overlay", "# Codex runtime overlay",
		"`msn_brief` — Milestone 3", "`tsk_brief` — Implement the slice", "Attempt: `3`",
		"Project: `sophon`", "Worktree: `/worktrees/m3`", "Branch: `task/m3`",
		"Base SHA: `0123456789abcdef0123456789abcdef01234567`", "Completion is independently verified.",
		"go test ./...", "sophon worker complete tsk_brief", filepath.Join(generator.BaseDir, "tsk_brief", "3", "result.json"),
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("brief omitted %q\n%s", required, text)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("brief permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestBriefGenerationMaterializesWorkerSkillSubsetAndTriggers(t *testing.T) {
	root := t.TempDir()
	generator := BriefGenerator{BaseDir: filepath.Join(root, "tasks"), SkillBaseDir: filepath.Join(root, "skills")}
	task := domain.Task{ID: "tsk_skills", MissionID: "msn_skills", Title: "Implement", Objective: "Build it.", DeliveryMode: domain.DeliveryBranch}
	path, err := generator.Render(BriefInput{
		MissionID: "msn_skills", MissionTitle: "Skills", MissionObjective: "Load skills.", Task: task, Attempt: 2,
		Project: "sophon", Worktree: "/worktrees/skills", Branch: "task/skills", BaseSHA: "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	skillDir, err := generator.SkillDir(task.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range runtimeprompts.WorkerSkills {
		skillPath := filepath.Join(skillDir, name, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			t.Errorf("worker skill %s was not materialized: %v", name, err)
		}
		if !strings.Contains(string(body), skillPath) {
			t.Errorf("worker brief omitted trigger path %s", skillPath)
		}
	}
	for _, name := range runtimeprompts.CommanderSkills {
		if name != "coding-guidelines" && name != "decision-lifecycle" && name != "diagnostic-reasoning" {
			if _, err := os.Stat(filepath.Join(skillDir, name, "SKILL.md")); !os.IsNotExist(err) {
				t.Errorf("worker materialized commander-only skill %s", name)
			}
		}
	}
}

func TestBriefGenerationPrefersEnvironmentPromptOverride(t *testing.T) {
	promptRoot := filepath.Join(t.TempDir(), "prompts")
	workerDir := filepath.Join(promptRoot, "workers")
	if err := os.MkdirAll(workerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"common.md":         "OVERRIDE COMMON WORKER PROMPT",
		"implementation.md": "OVERRIDE IMPLEMENTATION WORKER PROMPT",
	} {
		if err := os.WriteFile(filepath.Join(workerDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SOPHON_PROMPT_DIR", promptRoot)
	t.Setenv(runtimeprompts.LegacyOverrideEnv, "")
	generator := BriefGenerator{BaseDir: filepath.Join(t.TempDir(), "tasks")}
	path, err := generator.Render(BriefInput{
		MissionID: "msn_brief", MissionTitle: "Milestone 3", MissionObjective: "Reach ready safely.",
		Task:    domain.Task{ID: "tsk_brief", MissionID: "msn_brief", Title: "Implement the slice", Objective: "Build it.", DeliveryMode: domain.DeliveryBranch},
		Attempt: 1, Project: "sophon", Worktree: "/worktrees/m3", Branch: "task/m3", BaseSHA: "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(body); !strings.Contains(text, "OVERRIDE COMMON WORKER PROMPT") || !strings.Contains(text, "OVERRIDE IMPLEMENTATION WORKER PROMPT") || strings.Contains(text, "# Common worker prompt") {
		t.Fatalf("environment override was not preferred:\n%s", text)
	}
}
