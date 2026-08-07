package commander

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	runtimeprompts "parallel-intellect/prompts"
)

func TestPromptComposerUsesEmbeddedPromptsOutsideRepository(t *testing.T) {
	t.Setenv("PINTELLECT_PROMPT_DIR", "")
	root := t.TempDir()
	project := filepath.Join(root, "project")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	snapshot := db.CommanderLaunchContext{
		ProjectPath: project, ProjectName: "project",
		Mission: domain.Mission{ID: "msn_prompt", Objective: "resolve prompts"},
	}
	composed, err := (PromptComposer{}).Compose(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(composed, "# Parallel Intellect commander") {
		t.Fatalf("embedded prompt missing:\n%s", composed)
	}
	for _, rule := range []string{
		"A task must be a coherent, substantive unit of work worth a full worker",
		"Do not create micro-tasks for a single-function tweak, one-line edit",
		"split it along meaningful architectural or outcome",
		"Never change a registered project yourself.",
		"Workers never contact the operator directly.",
		"no budget binds unless it is",
		"pintellect wait --mission <id> --after-seq <sequence>",
		"Never sleep-poll.",
		"pintellect worker inspect TASK --attempt N --json",
		"pintellect task timeline TASK --json",
		"pintellect status --mission MISSION --json",
		"never paste raw JSON payloads or command dumps",
		"Never invent follow-on implementation work from inference",
		"A completed scout must leave a self-contained report",
		"The task attempt that owns implementation also owns its validation loop",
		"Worker success begins completion review; it does not end it.",
		"Every operator-facing mention of a PR",
	} {
		if !strings.Contains(composed, rule) {
			t.Errorf("embedded prompt omitted %q:\n%s", rule, composed)
		}
	}
	if strings.Contains(composed, "CWD PROMPT") {
		t.Fatalf("read prompt from current directory:\n%s", composed)
	}
}

func TestPromptComposerPrefersEnvironmentOverride(t *testing.T) {
	root := t.TempDir()
	promptRoot := filepath.Join(root, "prompts")
	commanderDir := filepath.Join(promptRoot, "commander")
	if err := os.MkdirAll(commanderDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commanderDir, "AGENTS.md"), []byte("OVERRIDE COMMANDER PROMPT"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PINTELLECT_PROMPT_DIR", promptRoot)
	composed, err := (PromptComposer{}).Compose(db.CommanderLaunchContext{
		ProjectPath: filepath.Join(root, "project"), ProjectName: "project",
		Mission: domain.Mission{ID: "msn_prompt", Objective: "resolve prompts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(composed, "OVERRIDE COMMANDER PROMPT") || strings.Contains(composed, "# Parallel Intellect commander") {
		t.Fatalf("environment override was not preferred:\n%s", composed)
	}
}

func TestPromptComposerIntakeCreatesMissionConversationally(t *testing.T) {
	promptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(promptDir, "AGENTS.md"), []byte("KNOWLEDGE DIGEST SIGNAL BASELINE"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: "prj_intake", Name: "example", Path: "/work/example"}
	composed, err := (PromptComposer{Dir: promptDir}).ComposeIntake(project, "/state/pintellect.db")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"KNOWLEDGE DIGEST SIGNAL BASELINE", "Mode: intake", "ask what we are working on",
		"pintellect mission create --project", project.Path, "--db \"/state/pintellect.db\"",
		"operator must never be asked to run mission create",
		"--operator-message <verbatim-operator-words>",
	} {
		if !strings.Contains(composed, fragment) {
			t.Errorf("intake prompt omitted %q:\n%s", fragment, composed)
		}
	}
	if strings.Contains(composed, "# Bound mission") {
		t.Fatalf("intake prompt invented a mission:\n%s", composed)
	}
}

func TestPromptComposerMaterializesFullSkillSetAndAddsTriggers(t *testing.T) {
	base := filepath.Join(t.TempDir(), "skills")
	composer := PromptComposer{SkillBaseDir: base}
	skillDir, err := composer.MaterializeSkills("csn_prompt")
	if err != nil {
		t.Fatal(err)
	}
	composed, err := composer.ComposeWithSkills(db.CommanderLaunchContext{
		ProjectPath: t.TempDir(), ProjectName: "project",
		Mission: domain.Mission{ID: "msn_prompt", Objective: "resolve prompts"},
	}, skillDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range runtimeprompts.CommanderSkills {
		path := filepath.Join(skillDir, name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("commander skill %s was not materialized: %v", name, err)
		}
		if !strings.Contains(composed, path) {
			t.Errorf("commander prompt omitted trigger path %s", path)
		}
	}
}
