// Package prompts provides the runtime prompt sets shipped with sophon.
package prompts

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	OverrideEnv = "SOPHON_PROMPT_DIR"
)

// CommanderSkills are available to a commander session. WorkerSkills are the
// deliberately smaller subset that applies to an implementation attempt.
var (
	CommanderSkills = []string{
		"agent-adapters", "coding-guidelines", "decision-lifecycle",
		"diagnostic-reasoning", "operator-authority", "proposal-execution", "recap", "status", "worker-recovery",
	}
	WorkerSkills = []string{"coding-guidelines", "decision-lifecycle", "diagnostic-reasoning"}
)

// Embedded contains the runtime prompt sets compiled into the binary.
//
//go:embed commander workers skills
var Embedded embed.FS

// Set returns a filesystem and root for a runtime prompt set. During
// development, SOPHON_PROMPT_DIR may point to a checkout's prompts
// directory; otherwise it reads the prompt set compiled into the binary.
func Set(name string) (fs.FS, string, error) {
	if !isRuntimeSet(name) {
		return nil, "", fmt.Errorf("unknown runtime prompt set %q", name)
	}
	if root := overrideRoot(); root != "" {
		dir := filepath.Join(root, name)
		info, err := os.Stat(dir)
		if err != nil {
			return nil, "", fmt.Errorf("inspect %s override %s: %w", name, dir, err)
		}
		if !info.IsDir() {
			return nil, "", fmt.Errorf("%s override %s is not a directory", name, dir)
		}
		return os.DirFS(root), name, nil
	}
	return Embedded, name, nil
}

func isRuntimeSet(name string) bool {
	return name == "commander" || name == "workers" || name == "skills"
}

// MaterializeSkills writes named embedded runtime skills to dir. The directory
// is intended to be unique to one live agent session and remains stable for
// that session's lifetime.
func MaterializeSkills(dir string, names []string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("skill directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create skill directory: %w", err)
	}
	promptFS, root, err := skillSet()
	if err != nil {
		return err
	}
	for _, name := range uniqueSkillNames(names) {
		if !knownSkill(name) {
			return fmt.Errorf("unknown runtime skill %q", name)
		}
		content, err := fs.ReadFile(promptFS, filepath.ToSlash(filepath.Join(root, name, "SKILL.md")))
		if err != nil {
			return fmt.Errorf("read skill %s: %w", name, err)
		}
		skillDir := filepath.Join(dir, name)
		if err := os.MkdirAll(skillDir, 0o700); err != nil {
			return fmt.Errorf("create skill %s directory: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o600); err != nil {
			return fmt.Errorf("write skill %s: %w", name, err)
		}
	}
	return nil
}

// SkillTriggers tells an agent exactly when to load each materialized skill.
// Native discovery is not available for these per-session directories.
func SkillTriggers(dir string, names []string) string {
	available := make(map[string]bool, len(names))
	for _, name := range names {
		available[name] = true
	}
	triggers := []struct{ situation, skill string }{
		{"a bug is reported", "diagnostic-reasoning"},
		{"a worker is wedged or stuck", "worker-recovery"},
		{"you find a decision that needs preserving", "decision-lifecycle"},
		{"an authority or operator-consent question arises", "operator-authority"},
		{"proposal, start, approval, or project-selection language arises", "proposal-execution"},
		{"a recap is requested", "recap"},
		{"a status or catch-up request is made", "status"},
		{"you are doing coding work", "coding-guidelines"},
		{"an agent runtime question arises", "agent-adapters"},
	}
	var body strings.Builder
	body.WriteString("\n\n## Session skill load triggers\n\n")
	body.WriteString("These skills are materialized for this session. When a trigger applies, read the listed absolute `SKILL.md` path and follow it.\n")
	for _, trigger := range triggers {
		if available[trigger.skill] {
			fmt.Fprintf(&body, "\n- %s: `%s`", trigger.situation, filepath.Join(dir, trigger.skill, "SKILL.md"))
		}
	}
	return body.String()
}

func skillSet() (fs.FS, string, error) {
	// Development prompt overrides commonly contain only the edited commander or
	// worker overlay. Skills remain available from the embedded release set until
	// the override supplies a complete skills directory.
	if root := overrideRoot(); root != "" {
		candidate := filepath.Join(root, "skills")
		info, err := os.Stat(candidate)
		if err == nil {
			if !info.IsDir() {
				return nil, "", fmt.Errorf("skills override %s is not a directory", candidate)
			}
			return os.DirFS(root), "skills", nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("inspect skills override %s: %w", candidate, err)
		}
	}
	return Embedded, "skills", nil
}

func overrideRoot() string {
	return strings.TrimSpace(os.Getenv(OverrideEnv))
}

func knownSkill(name string) bool {
	for _, candidate := range CommanderSkills {
		if name == candidate {
			return true
		}
	}
	return false
}

func uniqueSkillNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	sort.Strings(unique)
	return unique
}
