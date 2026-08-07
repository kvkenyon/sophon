package commander

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
)

func TestPromptComposerResolvesProjectThenInstallDirNeverCWD(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	install := filepath.Join(root, "install")
	outside := filepath.Join(root, "outside")
	for path, content := range map[string]string{
		filepath.Join(project, "prompts", "commander", "AGENTS.md"): "PROJECT PROMPT",
		filepath.Join(install, "prompts", "commander", "AGENTS.md"): "INSTALL PROMPT",
		filepath.Join(outside, "prompts", "commander", "AGENTS.md"): "CWD PROMPT",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
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
	composed, err := (PromptComposer{InstallDir: install}).Compose(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(composed, "PROJECT PROMPT") || strings.Contains(composed, "INSTALL PROMPT") || strings.Contains(composed, "CWD PROMPT") {
		t.Fatalf("project prompt resolution:\n%s", composed)
	}
	if err := os.RemoveAll(filepath.Join(project, "prompts")); err != nil {
		t.Fatal(err)
	}
	composed, err = (PromptComposer{InstallDir: install}).Compose(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(composed, "INSTALL PROMPT") || strings.Contains(composed, "CWD PROMPT") {
		t.Fatalf("install prompt resolution:\n%s", composed)
	}
}
