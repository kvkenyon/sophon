package prompts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedRuntimeSetsContainTheirContent(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	t.Setenv(LegacyOverrideEnv, "")
	for _, test := range []struct {
		set, file, want string
	}{
		{"commander", "AGENTS.md", "# Sophon commander"},
		{"workers", "common.md", "# Common worker prompt"},
		{"skills", "status/SKILL.md", "# Status"},
	} {
		promptFS, root, err := Set(test.set)
		if err != nil {
			t.Fatal(err)
		}
		body, err := fs.ReadFile(promptFS, root+"/"+test.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), test.want) {
			t.Errorf("%s/%s omitted %q", test.set, test.file, test.want)
		}
	}
}

func TestMaterializeSkillsWritesRequestedEmbeddedSet(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	t.Setenv(LegacyOverrideEnv, "")
	dir := filepath.Join(t.TempDir(), "session-skills")
	if err := MaterializeSkills(dir, CommanderSkills); err != nil {
		t.Fatal(err)
	}
	for _, name := range CommanderSkills {
		path := filepath.Join(dir, name, "SKILL.md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read materialized %s: %v", name, err)
		}
		embedded, err := fs.ReadFile(Embedded, "skills/"+name+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(embedded) {
			t.Errorf("materialized %s differs from embedded skill", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s permissions = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestLegacyPromptOverrideFallback(t *testing.T) {
	root := t.TempDir()
	commander := filepath.Join(root, "commander")
	if err := os.Mkdir(commander, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commander, "AGENTS.md"), []byte("legacy override"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(OverrideEnv, "")
	t.Setenv(LegacyOverrideEnv, root)
	promptFS, setRoot, err := Set("commander")
	if err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(promptFS, setRoot+"/AGENTS.md")
	if err != nil || string(body) != "legacy override" {
		t.Fatalf("legacy override body=%q err=%v", body, err)
	}
}

func TestNewPromptOverrideTakesPrecedence(t *testing.T) {
	current := t.TempDir()
	legacy := t.TempDir()
	for root, body := range map[string]string{current: "current", legacy: "legacy"} {
		dir := filepath.Join(root, "commander")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(OverrideEnv, current)
	t.Setenv(LegacyOverrideEnv, legacy)
	promptFS, setRoot, err := Set("commander")
	if err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(promptFS, setRoot+"/AGENTS.md")
	if err != nil || string(body) != "current" {
		t.Fatalf("override body=%q err=%v", body, err)
	}
}

func TestSkillTriggersUseOnlyRoleRelevantAbsolutePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	triggers := SkillTriggers(dir, WorkerSkills)
	for _, name := range WorkerSkills {
		if !strings.Contains(triggers, filepath.Join(dir, name, "SKILL.md")) {
			t.Errorf("worker trigger omitted %s: %s", name, triggers)
		}
	}
	for _, name := range CommanderSkills {
		if name != "coding-guidelines" && name != "decision-lifecycle" && name != "diagnostic-reasoning" && strings.Contains(triggers, name+"/SKILL.md") {
			t.Errorf("worker trigger included commander-only skill %s: %s", name, triggers)
		}
	}
}
