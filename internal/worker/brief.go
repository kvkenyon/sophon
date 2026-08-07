// Package worker owns the one-worker launch and structured completion slice.
package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	runtimeprompts "sophon/prompts"
)

type BriefInput struct {
	MissionID              domain.MissionID
	MissionTitle           string
	MissionObjective       string
	Task                   domain.Task
	Attempt                int
	Project                string
	Worktree               string
	Branch                 string
	BaseSHA                string
	ValidationRequirements []string
}

type BriefGenerator struct {
	BaseDir      string
	SkillBaseDir string
}

func DefaultTaskBaseDir() (string, error) {
	location, err := datahome.Resolve()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(location.Dir, "tasks"), nil
}

func (g BriefGenerator) AttemptDir(taskID domain.TaskID, attempt int) (string, error) {
	if taskID == "" || attempt < 1 {
		return "", errors.New("task and attempt are required")
	}
	base := g.BaseDir
	if base == "" {
		var err error
		base, err = DefaultTaskBaseDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(base, string(taskID), fmt.Sprintf("%d", attempt)), nil
}

func DefaultSkillBaseDir() (string, error) {
	location, err := datahome.Resolve()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(location.Dir, "skills"), nil
}

func (g BriefGenerator) SkillDir(taskID domain.TaskID, attempt int) (string, error) {
	if taskID == "" || attempt < 1 {
		return "", errors.New("task and attempt are required")
	}
	base := g.SkillBaseDir
	if base == "" {
		var err error
		base, err = DefaultSkillBaseDir()
		if err != nil {
			return "", err
		}
	}
	dir, err := filepath.Abs(filepath.Join(base, "worker", string(taskID), fmt.Sprintf("%d", attempt)))
	if err != nil {
		return "", fmt.Errorf("resolve worker skill directory: %w", err)
	}
	return dir, nil
}

func (g BriefGenerator) Render(in BriefInput) (string, error) {
	if in.MissionID == "" || in.Task.ID == "" || in.Attempt < 1 || in.Project == "" ||
		in.Worktree == "" || in.Branch == "" || in.BaseSHA == "" || strings.TrimSpace(in.Task.Objective) == "" {
		return "", errors.New("complete mission, task, lease, and objective facts are required")
	}
	dir, err := g.AttemptDir(in.Task.ID, in.Attempt)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create task brief directory: %w", err)
	}
	skillDir, err := g.SkillDir(in.Task.ID, in.Attempt)
	if err != nil {
		return "", err
	}
	if err := runtimeprompts.MaterializeSkills(skillDir, runtimeprompts.WorkerSkills); err != nil {
		return "", fmt.Errorf("materialize worker skills: %w", err)
	}
	resultPath := filepath.Join(dir, "result.json")
	criteria := in.Task.AcceptanceCriteria
	if len(criteria) == 0 {
		criteria = []domain.Criterion{{Description: "Satisfy the task objective without broadening scope."}}
	}
	validations := in.ValidationRequirements
	if len(validations) == 0 {
		validations = []string{"Run the repository's focused tests for changed behavior.", "Run the repository's required build and test commands."}
	}

	common, implementation, err := workerPromptOverlays()
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(common)
	body.WriteString("\n\n")
	body.WriteString(implementation)
	body.WriteString(runtimeprompts.SkillTriggers(skillDir, runtimeprompts.WorkerSkills))
	body.WriteString("\n\n# Codex runtime overlay\n\n")
	body.WriteString("Use Codex autonomously for this one implementation attempt. Treat this complete prompt as the generated brief; do not wait for a human.\n")
	body.WriteString("\n# Generated task brief\n\n")
	fmt.Fprintf(&body, "- Mission: `%s` — %s\n", in.MissionID, cleanLine(in.MissionTitle))
	fmt.Fprintf(&body, "- Mission objective: %s\n", cleanLine(in.MissionObjective))
	fmt.Fprintf(&body, "- Task: `%s` — %s\n", in.Task.ID, cleanLine(in.Task.Title))
	fmt.Fprintf(&body, "- Attempt: `%d`\n", in.Attempt)
	fmt.Fprintf(&body, "- Project: `%s`\n", in.Project)
	fmt.Fprintf(&body, "- Worktree: `%s`\n", in.Worktree)
	fmt.Fprintf(&body, "- Branch: `%s`\n", in.Branch)
	fmt.Fprintf(&body, "- Base SHA: `%s`\n", in.BaseSHA)
	fmt.Fprintf(&body, "- Delivery mode: `%s`\n", in.Task.DeliveryMode)
	body.WriteString("\n## Objective\n\n")
	body.WriteString(strings.TrimSpace(in.Task.Objective))
	body.WriteString("\n\n## Acceptance criteria\n\n")
	writeCriteria(&body, criteria)
	body.WriteString("\n## Relevant dependency results\n\n- None for this vertical slice.\n")
	body.WriteString("\n## Validation requirements\n\n")
	for _, validation := range validations {
		fmt.Fprintf(&body, "- %s\n", cleanLine(validation))
	}
	body.WriteString("\n## Permissions\n\n")
	body.WriteString("- Modify and commit files only in the assigned worktree.\n")
	fmt.Fprintf(&body, "- Write the structured result only to `%s`; this control-plane artifact is the sole write permitted outside the worktree.\n", resultPath)
	body.WriteString("- Run non-destructive local validation required by the project.\n")
	body.WriteString("\n## Forbidden actions\n\n")
	body.WriteString("- Do not create, return, or alter Treehouse leases or worktrees.\n")
	body.WriteString("- Do not push, merge, open a PR, or contact the operator.\n")
	body.WriteString("- Do not write project changes outside the assigned worktree.\n")
	body.WriteString("- Do not submit completion from any attempt other than the one above.\n")
	body.WriteString("\n## Completion instructions\n\n")
	fmt.Fprintf(&body, "1. Commit at least one new descendant of `%s` on `%s`.\n", in.BaseSHA, in.Branch)
	body.WriteString("2. Run the required validation and ensure the Git worktree is clean.\n")
	fmt.Fprintf(&body, "3. Write version 1 completion JSON to `%s` with exactly these fields: `version`, `status`, `summary`, `verification`, `changed_files`, and `risks`.\n", resultPath)
	body.WriteString("4. Submit exactly once:\n\n```bash\n")
	fmt.Fprintf(&body, "sophon worker complete %s \\\n  --attempt %d \\\n  --head-sha \"$(git rev-parse HEAD)\" \\\n  --result %q\n", in.Task.ID, in.Attempt, resultPath)
	body.WriteString("```\n")

	path := filepath.Join(dir, "brief.md")
	temporary, err := os.CreateTemp(dir, ".brief-*")
	if err != nil {
		return "", fmt.Errorf("create task brief: %w", err)
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.WriteString(body.String())
	}
	closeErr := temporary.Close()
	if err != nil {
		return "", fmt.Errorf("write task brief: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close task brief: %w", closeErr)
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", fmt.Errorf("publish task brief: %w", err)
	}
	return path, nil
}

func workerPromptOverlays() (string, string, error) {
	promptFS, root, err := runtimeprompts.Set("workers")
	if err != nil {
		return "", "", err
	}
	common, err := fs.ReadFile(promptFS, filepath.ToSlash(filepath.Join(root, "common.md")))
	if err != nil {
		return "", "", fmt.Errorf("read common worker prompt: %w", err)
	}
	implementation, err := fs.ReadFile(promptFS, filepath.ToSlash(filepath.Join(root, "implementation.md")))
	if err != nil {
		return "", "", fmt.Errorf("read implementation worker prompt: %w", err)
	}
	return strings.TrimSpace(string(common)), strings.TrimSpace(string(implementation)), nil
}

func cleanLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func writeCriteria(body *strings.Builder, criteria []domain.Criterion) {
	for _, criterion := range criteria {
		fmt.Fprintf(body, "- %s\n", cleanLine(criterion.Description))
	}
}
