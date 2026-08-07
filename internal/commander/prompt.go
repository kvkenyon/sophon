package commander

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sophon/internal/datahome"
	"sophon/internal/db"
	"sophon/internal/domain"
	runtimeprompts "sophon/prompts"
)

type PromptComposer struct {
	// Dir overrides prompt discovery. Absolute paths are used directly; relative
	// paths are resolved against the registered project and then InstallDir.
	Dir        string
	InstallDir string
	// SkillBaseDir overrides the parent directory for per-session skills.
	// Production defaults to ~/.sophon/skills.
	SkillBaseDir string
}

func (c PromptComposer) Compose(snapshot db.CommanderLaunchContext) (string, error) {
	return c.compose(snapshot, "")
}

func (c PromptComposer) ComposeWithSkills(snapshot db.CommanderLaunchContext, skillDir string) (string, error) {
	return c.compose(snapshot, skillDir)
}

func (c PromptComposer) compose(snapshot db.CommanderLaunchContext, skillDir string) (string, error) {
	baseline, err := c.promptSet(snapshot.ProjectPath)
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(baseline)
	if skillDir != "" {
		body.WriteString(runtimeprompts.SkillTriggers(skillDir, runtimeprompts.CommanderSkills))
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode commander mission snapshot: %w", err)
	}
	fmt.Fprintf(&body, `

# Bound mission

Mode: mission resume
Mission ID: %s
Project: %s
Project root: %s

## Objective

%s

## Acceptance criteria
`, snapshot.Mission.ID, snapshot.ProjectName, snapshot.ProjectPath, snapshot.Mission.Objective)
	if len(snapshot.Mission.AcceptanceCriteria) == 0 {
		body.WriteString("\n- No explicit acceptance criteria were recorded.\n")
	} else {
		for _, criterion := range snapshot.Mission.AcceptanceCriteria {
			fmt.Fprintf(&body, "\n- %s", criterion.Description)
		}
		body.WriteByte('\n')
	}
	if len(snapshot.OperatorMessages) > 0 {
		body.WriteString("\n## Recent operator direction\n")
		body.WriteString("\nThese are durable, chronological operator messages from this mission. Follow their current applicable intent; reconcile any conflict with structured state and ask when it is materially ambiguous.\n")
		for _, message := range snapshot.OperatorMessages {
			fmt.Fprintf(&body, "\n- [%s] %s", message.Kind, message.Message)
		}
		body.WriteByte('\n')
	}
	fmt.Fprintf(&body, "\n## Current mission state snapshot\n\n```json\n%s\n```\n", encoded)
	return strings.TrimSpace(body.String()) + "\n", nil
}

// ComposeIntake builds the same durable commander baseline without inventing
// a placeholder mission. The running agent binds itself by creating a real
// mission after the operator describes the work conversationally.
func (c PromptComposer) ComposeIntake(project domain.Project, databasePath string) (string, error) {
	return c.composeIntake(project, databasePath, "")
}

func (c PromptComposer) ComposeIntakeWithSkills(project domain.Project, databasePath, skillDir string) (string, error) {
	return c.composeIntake(project, databasePath, skillDir)
}

func (c PromptComposer) composeIntake(project domain.Project, databasePath, skillDir string) (string, error) {
	baseline, err := c.promptSet(project.Path)
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(baseline)
	if skillDir != "" {
		body.WriteString(runtimeprompts.SkillTriggers(skillDir, runtimeprompts.CommanderSkills))
	}
	dbArgument := ""
	if strings.TrimSpace(databasePath) != "" {
		dbArgument = fmt.Sprintf(" --db %q", databasePath)
	}
	fmt.Fprintf(&body, `

# Bound project intake

Mode: intake (there is no mission yet)
Project: %s
Project ID: %s
Project root: %s

Greet the operator briefly and ask what we are working on. After the operator
describes the task in natural language, infer a concise title, a concrete
objective, and sensible acceptance criteria. Then run:

    sophon mission create --project %q --title <title> --objective <objective> --operator-message <verbatim-operator-words> --acceptance <criterion>%s

Use repeated --acceptance arguments when useful. Read the returned mission ID,
treat it as your bound mission, and proceed to execute it through the existing
commander APIs. The operator must never be asked to run mission create. Do not
create a speculative mission before receiving the operator's task description.
Pass the operator's substantive words verbatim with --operator-message as well
as preserving them in the mission objective and criteria. This records the
direction before the intake conversation can be lost.
`, project.Name, project.ID, project.Path, project.Path, dbArgument)
	return strings.TrimSpace(body.String()) + "\n", nil
}

func (c PromptComposer) MaterializeSkills(sessionID domain.SessionID) (string, error) {
	if sessionID == "" {
		return "", errors.New("commander session ID is required")
	}
	base := c.SkillBaseDir
	if base == "" {
		location, err := datahome.Resolve()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(location.Dir, "skills")
	}
	dir, err := filepath.Abs(filepath.Join(base, "commander", string(sessionID)))
	if err != nil {
		return "", fmt.Errorf("resolve commander skill directory: %w", err)
	}
	if err := runtimeprompts.MaterializeSkills(dir, runtimeprompts.CommanderSkills); err != nil {
		return "", fmt.Errorf("materialize commander skills: %w", err)
	}
	return dir, nil
}

func (c PromptComposer) promptSet(projectPath string) (string, error) {
	if dir, err := c.resolveDir(projectPath); err == nil {
		return readPromptSet(os.DirFS(dir), ".")
	} else if strings.TrimSpace(c.Dir) != "" {
		return "", err
	}
	promptFS, root, err := runtimeprompts.Set("commander")
	if err != nil {
		return "", err
	}
	return readPromptSet(promptFS, root)
}

func readPromptSet(promptFS fs.FS, root string) (string, error) {
	var paths []string
	if err := fs.WalkDir(promptFS, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("read commander prompt set: %w", err)
	}
	if len(paths) == 0 {
		return "", errors.New("commander prompt set contains no markdown files")
	}
	sort.Strings(paths)
	var body strings.Builder
	for _, path := range paths {
		content, err := fs.ReadFile(promptFS, path)
		if err != nil {
			return "", fmt.Errorf("read commander prompt %s: %w", path, err)
		}
		fmt.Fprintf(&body, "\n\n<!-- commander prompt: %s -->\n%s", filepath.ToSlash(path), content)
	}
	return body.String(), nil
}

func (c PromptComposer) resolveDir(projectPath string) (string, error) {
	relative := strings.TrimSpace(c.Dir)
	if relative == "" {
		return "", os.ErrNotExist
	}
	if filepath.IsAbs(relative) {
		return relative, nil
	}
	candidates := make([]string, 0, 2)
	if projectPath = strings.TrimSpace(projectPath); projectPath != "" {
		candidates = append(candidates, filepath.Join(projectPath, relative))
	}
	if installDir := strings.TrimSpace(c.InstallDir); installDir != "" {
		candidates = append(candidates, filepath.Join(installDir, relative))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect commander prompt set %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("commander prompt set not found in explicit override (checked %s)", strings.Join(candidates, ", "))
}
