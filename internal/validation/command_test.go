package validation

import (
	"context"
	"strings"
	"testing"
)

func TestCommandValidatorRunsInWorktreeAndStructuresFailure(t *testing.T) {
	worktree := t.TempDir()
	passed, err := ShellValidator(ProjectValidation, "v1", "pwd").Run(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if passed.Status != Passed || passed.ExitCode != 0 || strings.TrimSpace(passed.Output) != worktree {
		t.Fatalf("passed result = %+v", passed)
	}

	failed, err := ShellValidator(Lint, "v1", "printf lint-failed; exit 7").Run(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != Failed || failed.ExitCode != 7 || failed.Output != "lint-failed" {
		t.Fatalf("failed result = %+v", failed)
	}
}
