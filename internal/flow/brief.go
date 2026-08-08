package flow

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"sophon/internal/datahome"
	"sophon/internal/store"
	runtimeprompts "sophon/prompts"
)

// renderBrief builds the worker's work order: the workers prompt overlays,
// materialized-skill triggers, and a generated section pinning the mission,
// attempt, Git identity, permissions, and the completion contract.
func (f *Flow) renderBrief(mission store.Mission, task store.Task, attempt int,
	worktree, branch, baseSHA string) (string, error) {
	homeDir, err := datahome.Dir()
	if err != nil {
		return "", err
	}
	skillDir := store.WorkerSkillDir(homeDir, task.ID, attempt)
	if err := runtimeprompts.MaterializeSkills(skillDir, runtimeprompts.WorkerSkills); err != nil {
		return "", fmt.Errorf("materialize worker skills: %w", err)
	}
	common, implementation, err := workerPromptOverlays()
	if err != nil {
		return "", err
	}
	resultPath := filepath.Join(store.AttemptDir(homeDir, mission.ID, task.ID, attempt), "result.json")
	validationCommand := strings.TrimSpace(task.ValidationCommand)
	if validationCommand == "" {
		validationCommand = "none configured"
	}

	var body strings.Builder
	body.WriteString(common)
	body.WriteString("\n\n")
	body.WriteString(implementation)
	body.WriteString(runtimeprompts.SkillTriggers(skillDir, runtimeprompts.WorkerSkills))
	body.WriteString("\n\n# Codex runtime overlay\n\n")
	body.WriteString("Use Codex autonomously for this one implementation attempt. Treat this complete prompt as the generated brief; do not wait for a human.\n")
	body.WriteString("\n# Generated task brief\n\n")
	fmt.Fprintf(&body, "- Mission: `%s` — %s\n", mission.ID, cleanLine(mission.Title))
	fmt.Fprintf(&body, "- Mission objective: %s\n", cleanLine(mission.Objective))
	fmt.Fprintf(&body, "- Task: `%s` — %s\n", task.ID, cleanLine(task.Title))
	fmt.Fprintf(&body, "- Attempt: `%d`\n", attempt)
	fmt.Fprintf(&body, "- Project: `%s`\n", mission.ProjectPath)
	fmt.Fprintf(&body, "- Worktree: `%s`\n", worktree)
	fmt.Fprintf(&body, "- Branch: `%s`\n", branch)
	fmt.Fprintf(&body, "- Base SHA: `%s`\n", baseSHA)
	fmt.Fprintf(&body, "- Delivery mode: `%s`\n", task.DeliveryMode)
	fmt.Fprintf(&body, "- Validation command: `%s`\n", validationCommand)
	body.WriteString("\n## Objective\n\n")
	body.WriteString(strings.TrimSpace(mission.Objective))
	body.WriteString("\n\n## Permissions\n\n")
	body.WriteString("- Modify and commit files only in the assigned worktree.\n")
	fmt.Fprintf(&body, "- Write the structured result only to `%s`; this control-plane artifact is the sole write permitted outside the worktree.\n", resultPath)
	body.WriteString("- Run non-destructive local validation required by the project.\n")
	body.WriteString("\n## Forbidden actions\n\n")
	body.WriteString("- Do not create, return, or alter Treehouse leases or worktrees.\n")
	body.WriteString("- Do not push, merge, open a PR, or contact the operator.\n")
	body.WriteString("- Do not write project changes outside the assigned worktree.\n")
	body.WriteString("- Do not submit completion from any attempt other than the one above.\n")
	body.WriteString("\n## Completion contract\n\n")
	fmt.Fprintf(&body, "1. Commit at least one new descendant of `%s` on `%s`.\n", baseSHA, branch)
	body.WriteString("2. Run the required validation and ensure the Git worktree is clean.\n")
	fmt.Fprintf(&body, "3. Write version 1 completion JSON to `%s` with exactly these fields: `version`, `status`, `summary`, `verification`, `changed_files`, and `risks`.\n", resultPath)
	body.WriteString("4. Finish by submitting exactly once:\n\n```bash\n")
	fmt.Fprintf(&body, "sophon worker complete %s --attempt %d --head-sha \"$(git rev-parse HEAD)\" --result %q\n", task.ID, attempt, resultPath)
	body.WriteString("```\n")
	return body.String(), nil
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
