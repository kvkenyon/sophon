package commander

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
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
		"pintellect wait --mission <id> --after-seq <sequence>",
		"Never sleep-poll.",
		"pintellect worker inspect TASK --attempt N --json",
		"pintellect task timeline TASK --json",
		"pintellect status --mission MISSION --json",
		"never paste raw JSON payloads or command dumps",
		"Never invent follow-on implementation work from inference",
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
