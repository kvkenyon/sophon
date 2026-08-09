package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestWorkspaceInitAndProjectOperations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	marker, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Init(root)
	if err != nil || again != marker {
		t.Fatalf("idempotent init = %+v, %v; want %+v", again, err, marker)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace root became a Git repository: %v", err)
	}
	inspector := Inspector{GitBinary: "git"}
	empty, err := inspector.Create(context.Background(), root, "empty-local", "trunk")
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Unborn || empty.Branch != "trunk" || empty.Remote != "" {
		t.Fatalf("empty project = %+v", empty)
	}

	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "init", "-b", "main")
	gitRun(t, source, "config", "user.name", "Workspace Test")
	gitRun(t, source, "config", "user.email", "workspace@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "add", "README.md")
	gitRun(t, source, "commit", "-m", "Initial fixture")
	remote, err := inspector.Clone(context.Background(), root, "remote-backed", source)
	if err != nil {
		t.Fatal(err)
	}
	if remote.Unborn || remote.Remote == "" || remote.Head == "" {
		t.Fatalf("remote project = %+v", remote)
	}
	projects, err := inspector.List(context.Background(), root)
	if err != nil || len(projects) != 2 || projects[0].Key != "empty-local" || projects[1].Key != "remote-backed" {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
	if adopted, err := inspector.Add(context.Background(), root, "remote-backed"); err != nil || adopted.Identity != remote.Identity {
		t.Fatalf("adopt existing child = %+v, %v", adopted, err)
	}
}

func TestWorkspaceRefusesUnsafeLayoutAndIdentityDrift(t *testing.T) {
	inspector := Inspector{GitBinary: "git"}
	t.Run("root as project", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "workspace")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		if _, err := inspector.Resolve(context.Background(), root, "."); err == nil {
			t.Fatal("workspace root was accepted as a project")
		}
	})
	t.Run("unrelated root", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(root); err == nil {
			t.Fatal("captured unrelated directory contents")
		}
	})
	t.Run("conflicting marker", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "workspace")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(`{"version":1,"id":"workspace_conflict","root":"/elsewhere","projects_dir":"projects","created_at":"2026-08-09T00:00:00Z"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(root); err == nil {
			t.Fatal("conflicting marker was accepted")
		}
	})
	t.Run("unsafe permissions", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "workspace")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(root); err == nil {
			t.Fatal("unsafe workspace permissions were accepted")
		}
	})
	t.Run("nested workspace", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "outer")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(filepath.Join(root, "nested")); err == nil {
			t.Fatal("nested workspace was accepted")
		}
	})
	t.Run("symlink child", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "workspace")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		gitRun(t, external, "init", "-b", "main")
		if err := os.Symlink(external, filepath.Join(root, ProjectsDir, "escape")); err != nil {
			t.Fatal(err)
		}
		if _, err := inspector.Resolve(context.Background(), root, "escape"); err == nil {
			t.Fatal("symlink escape was accepted")
		}
	})
	t.Run("case collision", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "workspace")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		upper := filepath.Join(root, ProjectsDir, "Project")
		if err := os.Mkdir(upper, 0o700); err != nil {
			t.Fatal(err)
		}
		gitRun(t, upper, "init", "-b", "main")
		if _, err := inspector.Create(context.Background(), root, "project", "main"); err == nil {
			t.Fatal("case collision was accepted")
		}
	})
	t.Run("replacement identity", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "workspace")
		if _, err := Init(root); err != nil {
			t.Fatal(err)
		}
		original, err := inspector.Create(context.Background(), root, "service", "main")
		if err != nil {
			t.Fatal(err)
		}
		moved := filepath.Join(root, ProjectsDir, ".moved-service")
		if err := os.Rename(original.Path, moved); err != nil {
			t.Fatal(err)
		}
		replacement, err := inspector.Create(context.Background(), root, "service", "main")
		if err != nil {
			t.Fatal(err)
		}
		if ValidatePinned(replacement, original.WorkspaceID, original.Key, original.Path, original.Identity) == nil {
			t.Fatal("project path replacement preserved pinned identity")
		}
	})
}

func TestWorkspaceRefusesDuplicateGitCommonIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if _, err := Init(root); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{GitBinary: "git"}
	project, err := inspector.Create(context.Background(), root, "primary", "main")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, project.Path, "config", "user.name", "Workspace Test")
	gitRun(t, project.Path, "config", "user.email", "workspace@example.invalid")
	gitRun(t, project.Path, "commit", "--allow-empty", "-m", "Initial")
	duplicate := filepath.Join(root, ProjectsDir, "duplicate")
	gitRun(t, project.Path, "worktree", "add", "-b", "duplicate", duplicate)
	if _, err := inspector.List(context.Background(), root); err == nil || !strings.Contains(err.Error(), "share one Git identity") {
		t.Fatalf("duplicate Git identity error = %v", err)
	}
}
