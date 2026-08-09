package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func bootstrapGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func unbornRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bootstrapGit(t, dir, "init", "-b", branch)
	return dir
}

func bootstrapSpec(branch string) BootstrapSpec {
	return BootstrapSpec{Branch: branch, Ref: "refs/heads/" + branch, CommitMessage: "Initialize project history",
		AuthorName: "Project Contributors", AuthorEmail: "contributors@localhost.invalid",
		AuthoredAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
}

func TestEmptyRepositoryBootstrapIsDeterministicConcurrentAndRemoteFree(t *testing.T) {
	project := unbornRepo(t, "trunk")
	client := NewClient()
	state, err := client.InspectBootstrap(context.Background(), project)
	if err != nil || !state.Needed || state.Branch != "trunk" {
		t.Fatalf("bootstrap state = %+v, %v", state, err)
	}
	spec := bootstrapSpec("trunk")
	var wg sync.WaitGroup
	results := make([]BootstrapResult, 8)
	errorsSeen := make([]error, len(results))
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errorsSeen[index] = client.CreateBootstrap(context.Background(), project, spec)
		}(index)
	}
	wg.Wait()
	for index, result := range results {
		if errorsSeen[index] != nil || result.CommitSHA == "" || result.CommitSHA != results[0].CommitSHA {
			t.Fatalf("concurrent result %d = %+v, %v", index, result, errorsSeen[index])
		}
	}
	if parents := bootstrapGit(t, project, "rev-list", "--parents", "-n", "1", "HEAD"); parents != results[0].CommitSHA {
		t.Fatalf("bootstrap has parents: %s", parents)
	}
	if tree := bootstrapGit(t, project, "ls-tree", "--name-only", "HEAD"); tree != "" {
		t.Fatalf("bootstrap tree = %q", tree)
	}
	if remotes := bootstrapGit(t, project, "remote"); remotes != "" {
		t.Fatalf("bootstrap created remote %q", remotes)
	}
	again, err := client.CreateBootstrap(context.Background(), project, spec)
	if err != nil || again.CommitSHA != results[0].CommitSHA {
		t.Fatalf("restart convergence = %+v, %v", again, err)
	}
}

func TestEmptyRepositoryBootstrapRefusals(t *testing.T) {
	client := NewClient()
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"untracked content", func(t *testing.T, dir string) { os.WriteFile(filepath.Join(dir, "product.txt"), []byte("x"), 0o600) }},
		{"ignored content", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("ignored.txt\n"), 0o600)
			os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x"), 0o600)
		}},
		{"symlink content", func(t *testing.T, dir string) { os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "linked")) }},
		{"symlink Git internals", func(t *testing.T, dir string) {
			os.Symlink(filepath.Join(dir, ".git", "HEAD"), filepath.Join(dir, ".git", "linked-head"))
		}},
		{"alternate object storage", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ".git", "objects", "info", "alternates"), []byte(filepath.Join(dir, ".git", "objects")+"\n"), 0o600)
		}},
		{"unsafe config", func(t *testing.T, dir string) { bootstrapGit(t, dir, "config", "core.hooksPath", "hooks") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := unbornRepo(t, "main")
			test.setup(t, project)
			if _, err := client.InspectBootstrap(context.Background(), project); err == nil {
				t.Fatal("unsafe empty repository was accepted")
			}
			if _, err := exec.Command("git", "-C", project, "rev-parse", "--verify", "HEAD").CombinedOutput(); err == nil {
				t.Fatal("refusal created a commit")
			}
		})
	}
	t.Run("ambient Git override", func(t *testing.T) {
		project := unbornRepo(t, "main")
		t.Setenv("GIT_WORK_TREE", project)
		if _, err := client.InspectBootstrap(context.Background(), project); !errors.Is(err, ErrUnsafeGitContext) {
			t.Fatalf("ambient override error = %v", err)
		}
	})
	t.Run("unusual git file", func(t *testing.T) {
		primary := unbornRepo(t, "main")
		bootstrapGit(t, primary, "config", "user.name", "Bootstrap Test")
		bootstrapGit(t, primary, "config", "user.email", "bootstrap@example.invalid")
		bootstrapGit(t, primary, "commit", "--allow-empty", "-m", "Initial")
		linked := filepath.Join(t.TempDir(), "linked")
		bootstrapGit(t, primary, "worktree", "add", "-b", "linked", linked)
		if _, err := client.InspectBootstrap(context.Background(), linked); err == nil {
			t.Fatal("linked-worktree .git file was accepted as empty bootstrap")
		}
	})
}

func TestBootstrapRefusesConcurrentBranchAndDrift(t *testing.T) {
	project := unbornRepo(t, "main")
	client := NewClient()
	spec := bootstrapSpec("main")
	tree := bootstrapGit(t, project, "mktree")
	command := exec.Command("git", "-C", project, "commit-tree", tree)
	command.Stdin = strings.NewReader("Unrelated root\n")
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Other", "GIT_AUTHOR_EMAIL=other@example.invalid",
		"GIT_COMMITTER_NAME=Other", "GIT_COMMITTER_EMAIL=other@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	other := strings.TrimSpace(string(output))
	bootstrapGit(t, project, "update-ref", spec.Ref, other)
	if _, err := client.CreateBootstrap(context.Background(), project, spec); !errors.Is(err, ErrBranchAppeared) {
		t.Fatalf("concurrent branch error = %v", err)
	}
}
