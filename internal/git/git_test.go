package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCompletionRequiresNewDescendantAndCleanTree(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "Sophon Test")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(repo, "file.txt"), "base\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "base")
	base := runGit(t, repo, "rev-parse", "HEAD")

	client := NewClient()
	if _, err := client.VerifyCompletion(ctx, repo, base); !errors.Is(err, ErrNoNewCommit) {
		t.Fatalf("unchanged HEAD error = %v, want ErrNoNewCommit", err)
	}
	writeFile(t, filepath.Join(repo, "file.txt"), "base\nnew\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "new")
	completion, err := client.VerifyCompletion(ctx, repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if completion.BaseSHA != base || completion.HeadSHA == base || completion.Branch != "main" {
		t.Fatalf("completion = %+v", completion)
	}

	writeFile(t, filepath.Join(repo, "dirty.txt"), "untracked\n")
	if _, err := client.VerifyCompletion(ctx, repo, base); !errors.Is(err, ErrDirtyTree) {
		t.Fatalf("dirty tree error = %v, want ErrDirtyTree", err)
	}
}

func TestCreateTaskBranchAttachesDetachedWorktreeAtRecordedBase(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "Sophon Test")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(repo, "file.txt"), "base\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "base")
	base := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "--detach", "HEAD")

	snapshot, err := NewClient().CreateTaskBranch(ctx, repo, "sophon/task")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Head != base || snapshot.Branch != "sophon/task" || !snapshot.Clean {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := runGit(t, repo, "symbolic-ref", "--short", "HEAD"); got != snapshot.Branch {
		t.Fatalf("HEAD branch = %q, want %q", got, snapshot.Branch)
	}
}

func TestVerifyCompletionRejectsUnrelatedHead(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "Sophon Test")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(repo, "file.txt"), "base\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "base")
	base := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "--orphan", "unrelated")
	runGit(t, repo, "rm", "-f", "file.txt")
	writeFile(t, filepath.Join(repo, "other.txt"), "other\n")
	runGit(t, repo, "add", "other.txt")
	runGit(t, repo, "commit", "-m", "unrelated")

	if _, err := NewClient().VerifyCompletion(ctx, repo, base); !errors.Is(err, ErrNotDescendant) {
		t.Fatalf("unrelated HEAD error = %v, want ErrNotDescendant", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
