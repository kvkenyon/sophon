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
// attempt, Git identity, permissions, and the completion contract. homeDir is
// the exact resolved data home; the completion command pins it explicitly so
// a runtime that drops inherited environment still publishes to the assigned
// store.
func (f *Flow) renderBrief(homeDir string, mission store.Mission, task store.Task, attempt int,
	worktree, branch, baseSHA string, correction *store.Correction) (string, error) {
	skillDir := store.WorkerSkillDir(homeDir, task.ID, attempt)
	if err := runtimeprompts.MaterializeSkills(skillDir, runtimeprompts.WorkerSkills); err != nil {
		return "", fmt.Errorf("materialize worker skills: %w", err)
	}
	common, implementation, err := workerPromptOverlays()
	if err != nil {
		return "", err
	}
	resultPath := filepath.Join(store.AttemptDir(homeDir, mission.ID, task.ID, attempt), store.CompletionSubmissionName)
	reportPath := filepath.Join(store.AttemptDir(homeDir, mission.ID, task.ID, attempt), store.ReportSubmissionName)
	validationCommand := strings.TrimSpace(task.ValidationCommand)
	if validationCommand == "" {
		validationCommand = "none configured"
	}
	reviewPosture, err := store.EffectiveReviewPosture(task)
	if err != nil {
		return "", fmt.Errorf("derive task review posture: %w", err)
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
	fmt.Fprintf(&body, "- Public delivery title: %s\n", cleanLine(task.Title))
	fmt.Fprintf(&body, "- Public delivery branch: `%s`\n", task.DeliveryBranch)
	fmt.Fprintf(&body, "- Revision: `%d`\n", task.CurrentRevision)
	fmt.Fprintf(&body, "- Attempt: `%d`\n", attempt)
	fmt.Fprintf(&body, "- Project: `%s`\n", mission.ProjectPath)
	fmt.Fprintf(&body, "- Worktree: `%s`\n", worktree)
	fmt.Fprintf(&body, "- Branch: `%s`\n", branch)
	fmt.Fprintf(&body, "- Base SHA: `%s`\n", baseSHA)
	fmt.Fprintf(&body, "- Delivery mode: `%s`\n", task.DeliveryMode)
	fmt.Fprintf(&body, "- Read the Code review: `%s`\n", reviewPosture)
	fmt.Fprintf(&body, "- Validation command: `%s`\n", validationCommand)
	body.WriteString("\n## Objective\n\n")
	body.WriteString(strings.TrimSpace(task.Objective))
	if correction != nil {
		body.WriteString("\n\n## Accepted correction feedback\n\n")
		body.WriteString(strings.TrimSpace(correction.Objective))
		body.WriteString("\n\n## Correction boundary\n\n")
		fmt.Fprintf(&body, "- Continue the existing pull request `%s` from exact public head `%s`.\n", correction.PRURL, correction.BaseSHA)
		fmt.Fprintf(&body, "- Make only the bounded correction beyond revision %d; do not recreate or rebase the already-delivered history.\n", correction.PriorRevision)
		body.WriteString("- The result must be a strict descendant of the exact correction base above.\n")
		body.WriteString("- Do not push, update the pull request, deliver, force-push, or create a replacement pull request.\n")
	}
	body.WriteString("\n\n## Permissions\n\n")
	body.WriteString("- Modify and commit files only in the assigned worktree.\n")
	fmt.Fprintf(&body, "- Write a structured completion submission only to `%s`, or typed non-completion submission only to `%s`; these staging artifacts are the sole writes permitted outside the worktree.\n", resultPath, reportPath)
	body.WriteString("- Run non-destructive local validation required by the project.\n")
	body.WriteString("\n## Forbidden actions\n\n")
	body.WriteString("- Do not create, return, or alter Treehouse leases or worktrees.\n")
	body.WriteString("- Do not push, force-push, merge, open or update a PR, or contact the operator.\n")
	body.WriteString("- Do not write project changes outside the assigned worktree.\n")
	body.WriteString("- Do not submit completion from any attempt other than the one above.\n")
	body.WriteString("- Do not put Sophon branding, task/attempt IDs, private paths, runtime details, or orchestration prose in commit messages; commits must read as ordinary public-quality product history.\n")
	body.WriteString("\n## Completion contract\n\n")
	body.WriteString("For meaningful phase transitions only, send optional non-authoritative progress with this data-home-pinned form; choose only the stable phases `investigating`, `implementing`, `testing`, `waiting`, or `blocked`, and keep the note concise:\n\n```bash\n")
	fmt.Fprintf(&body, "%s=%s sophon worker progress %s --attempt %d --phase testing --message 'required validation started'\n",
		datahome.OverrideEnv, shellQuote(homeDir), task.ID, attempt)
	body.WriteString("```\n\nA missing monitor is nonfatal; continue to canonical completion or report publication.\n\n")
	fmt.Fprintf(&body, "1. Commit at least one new descendant of `%s` on `%s` using a concise public-quality subject and product-focused body with no orchestration language.\n", baseSHA, branch)
	body.WriteString("2. Run the required validation and ensure the Git worktree is clean.\n")
	fmt.Fprintf(&body, "3. Write version 1 completion JSON to `%s` with exactly these fields: `version`, `status`, `summary`, `verification`, `changed_files`, and `risks`.\n", resultPath)
	body.WriteString("4. Finish by submitting exactly once, with this exact command (it pins the assigned Sophon data home):\n\n```bash\n")
	fmt.Fprintf(&body, "%s=%s sophon worker complete %s --attempt %d --head-sha \"$(git rev-parse HEAD)\" --result %s\n",
		datahome.OverrideEnv, shellQuote(homeDir), task.ID, attempt, shellQuote(resultPath))
	body.WriteString("```\n\nFor `scope-mismatch` or `blocked`, do not write completion JSON. Write version 1 typed report JSON to the report submission path and publish it with this exact command:\n\n```bash\n")
	fmt.Fprintf(&body, "%s=%s sophon worker report %s --attempt %d --head-sha \"$(git rev-parse HEAD)\" --report %s\n",
		datahome.OverrideEnv, shellQuote(homeDir), task.ID, attempt, shellQuote(reportPath))
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

// shellQuote renders a value as a single POSIX-shell-quoted word so paths
// containing spaces or shell metacharacters survive verbatim.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
