package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrNotEmptyRepository = errors.New("unborn repository contains project, untracked, ignored, symlink, or unusual Git content")
	ErrBranchAppeared     = errors.New("bootstrap branch appeared concurrently at a different commit")
	ErrUnsafeGitContext   = errors.New("unsafe Git context for empty repository bootstrap")
)

type BootstrapState struct {
	Needed bool
	Branch string
	Ref    string
	Head   string
}

type BootstrapSpec struct {
	Branch        string
	Ref           string
	CommitMessage string
	AuthorName    string
	AuthorEmail   string
	AuthoredAt    time.Time
}

type BootstrapResult struct {
	Branch    string
	Ref       string
	CommitSHA string
}

// InspectBootstrap detects only a conventional, truly empty, initialized Git
// repository. Existing repositories simply report their current HEAD. An
// unborn repository with any content or unusual layout is refused rather than
// interpreted or modified.
func (c *Client) InspectBootstrap(ctx context.Context, projectPath string) (BootstrapState, error) {
	if err := safeBootstrapEnvironment(); err != nil {
		return BootstrapState{}, err
	}
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return BootstrapState{}, err
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return BootstrapState{}, fmt.Errorf("%w: project root must be a real directory", ErrUnsafeGitContext)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return BootstrapState{}, fmt.Errorf("%w: cannot resolve project root", ErrUnsafeGitContext)
	}
	abs = filepath.Clean(real)
	top, err := c.bootstrapOutput(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return BootstrapState{}, fmt.Errorf("%w: not a valid worktree: %v", ErrUnsafeGitContext, err)
	}
	top, err = filepath.Abs(top)
	if err != nil || filepath.Clean(top) != abs {
		return BootstrapState{}, fmt.Errorf("%w: wrong repository top-level", ErrUnsafeGitContext)
	}
	gitEntry := filepath.Join(abs, ".git")
	gitInfo, err := os.Lstat(gitEntry)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return BootstrapState{}, fmt.Errorf("%w: empty bootstrap requires a conventional .git directory", ErrUnsafeGitContext)
	}
	for _, query := range [][]string{{"rev-parse", "--path-format=absolute", "--git-dir"}, {"rev-parse", "--path-format=absolute", "--git-common-dir"}} {
		observed, queryErr := c.bootstrapOutput(ctx, abs, query...)
		if queryErr != nil {
			return BootstrapState{}, fmt.Errorf("%w: cannot resolve conventional Git layout", ErrUnsafeGitContext)
		}
		observed, queryErr = filepath.EvalSymlinks(observed)
		if queryErr != nil || filepath.Clean(observed) != gitEntry {
			return BootstrapState{}, fmt.Errorf("%w: Git directory is shared, redirected, or unusual", ErrUnsafeGitContext)
		}
	}
	if err := filepath.WalkDir(gitEntry, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: Git internals contain symlink %s", ErrUnsafeGitContext, path)
		}
		return nil
	}); err != nil {
		return BootstrapState{}, err
	}
	if _, err := os.Lstat(filepath.Join(gitEntry, "objects", "info", "alternates")); err == nil {
		return BootstrapState{}, fmt.Errorf("%w: alternate object storage is configured", ErrUnsafeGitContext)
	} else if !errors.Is(err, os.ErrNotExist) {
		return BootstrapState{}, err
	}
	if bare, _ := c.bootstrapOutput(ctx, abs, "config", "--local", "--bool", "core.bare"); bare == "true" {
		return BootstrapState{}, fmt.Errorf("%w: bare repository", ErrUnsafeGitContext)
	}
	for _, key := range []string{"core.worktree", "extensions.worktreeConfig", "core.hooksPath"} {
		if value, configErr := c.bootstrapOutput(ctx, abs, "config", "--local", "--get", key); configErr == nil && value != "" {
			return BootstrapState{}, fmt.Errorf("%w: local %s is configured", ErrUnsafeGitContext, key)
		}
	}
	if head, headErr := c.bootstrapOutput(ctx, abs, "rev-parse", "--verify", "HEAD"); headErr == nil {
		branch, _ := c.bootstrapOutput(ctx, abs, "symbolic-ref", "--short", "HEAD")
		return BootstrapState{Head: head, Branch: branch}, nil
	}
	ref, err := c.bootstrapOutput(ctx, abs, "symbolic-ref", "-q", "HEAD")
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") || strings.TrimPrefix(ref, "refs/heads/") == "" {
		return BootstrapState{}, fmt.Errorf("%w: unborn HEAD is detached or malformed", ErrUnsafeGitContext)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return BootstrapState{}, err
	}
	if len(entries) != 1 || entries[0].Name() != ".git" || !entries[0].IsDir() {
		return BootstrapState{}, ErrNotEmptyRepository
	}
	status, err := c.bootstrapOutput(ctx, abs, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching")
	if err != nil || status != "" {
		return BootstrapState{}, ErrNotEmptyRepository
	}
	return BootstrapState{Needed: true, Branch: strings.TrimPrefix(ref, "refs/heads/"), Ref: ref}, nil
}

// CreateBootstrap creates one deterministic empty commit object, then uses an
// update-ref compare-and-swap from an absent ref. Re-running after a crash
// verifies and returns the exact same root; a concurrently different branch
// is never overwritten.
func (c *Client) CreateBootstrap(ctx context.Context, projectPath string, spec BootstrapSpec) (BootstrapResult, error) {
	if spec.Branch == "" || spec.Ref != "refs/heads/"+spec.Branch || strings.TrimSpace(spec.CommitMessage) == "" ||
		strings.TrimSpace(spec.AuthorName) == "" || strings.TrimSpace(spec.AuthorEmail) == "" || spec.AuthoredAt.IsZero() {
		return BootstrapResult{}, errors.New("bootstrap specification is incomplete")
	}
	state, err := c.InspectBootstrap(ctx, projectPath)
	if err != nil {
		return BootstrapResult{}, err
	}
	if !state.Needed {
		return c.verifyBootstrap(ctx, projectPath, spec, state.Head)
	}
	if state.Ref != spec.Ref || state.Branch != spec.Branch {
		return BootstrapResult{}, fmt.Errorf("%w: HEAD moved from %s to %s", ErrBranchAppeared, spec.Ref, state.Ref)
	}
	tree, err := c.input(ctx, projectPath, nil, nil, "mktree")
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("create empty Git tree: %w", err)
	}
	timestamp := spec.AuthoredAt.UTC().Format(time.RFC3339)
	environment := map[string]string{
		"GIT_AUTHOR_NAME": spec.AuthorName, "GIT_AUTHOR_EMAIL": spec.AuthorEmail, "GIT_AUTHOR_DATE": timestamp,
		"GIT_COMMITTER_NAME": spec.AuthorName, "GIT_COMMITTER_EMAIL": spec.AuthorEmail, "GIT_COMMITTER_DATE": timestamp,
	}
	commit, err := c.input(ctx, projectPath, []byte(spec.CommitMessage+"\n"), environment, "commit-tree", tree)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("create empty root commit: %w", err)
	}
	zero := strings.Repeat("0", len(commit))
	if _, err := c.input(ctx, projectPath, nil, nil, "update-ref", spec.Ref, commit, zero); err != nil {
		observed, observeErr := c.bootstrapOutput(ctx, projectPath, "rev-parse", "--verify", "HEAD")
		if observeErr != nil || !strings.EqualFold(observed, commit) {
			return BootstrapResult{}, fmt.Errorf("%w: %v", ErrBranchAppeared, err)
		}
	}
	return c.verifyBootstrap(ctx, projectPath, spec, commit)
}

func (c *Client) verifyBootstrap(ctx context.Context, projectPath string, spec BootstrapSpec, expected string) (BootstrapResult, error) {
	head, err := c.bootstrapOutput(ctx, projectPath, "rev-parse", "--verify", "HEAD")
	if err != nil || !strings.EqualFold(head, expected) {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap HEAD does not match intent", ErrBranchAppeared)
	}
	ref, err := c.bootstrapOutput(ctx, projectPath, "symbolic-ref", "-q", "HEAD")
	if err != nil || ref != spec.Ref {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap ref does not match intent", ErrBranchAppeared)
	}
	parents, err := c.bootstrapOutput(ctx, projectPath, "rev-list", "--parents", "-n", "1", head)
	if err != nil || parents != head {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap commit is not an initial root", ErrBranchAppeared)
	}
	tree, err := c.bootstrapOutput(ctx, projectPath, "ls-tree", "--name-only", head)
	if err != nil || tree != "" {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap root contains files", ErrBranchAppeared)
	}
	message, err := c.bootstrapOutput(ctx, projectPath, "show", "-s", "--format=%B", head)
	if err != nil || strings.TrimSpace(message) != strings.TrimSpace(spec.CommitMessage) {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap commit message does not match intent", ErrBranchAppeared)
	}
	return BootstrapResult{Branch: spec.Branch, Ref: spec.Ref, CommitSHA: head}, nil
}

func (c *Client) input(ctx context.Context, path string, stdin []byte, extra map[string]string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, c.binary(), append([]string{"-C", path}, args...)...)
	command.Env = safeGitEnvironment(os.Environ(), extra)
	command.Stdin = bytes.NewReader(stdin)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func (c *Client) bootstrapOutput(ctx context.Context, path string, args ...string) (string, error) {
	return c.input(ctx, path, nil, nil, args...)
}

func safeBootstrapEnvironment() error {
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE"} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("%w: %s is set", ErrUnsafeGitContext, key)
		}
	}
	return nil
}

func safeGitEnvironment(existing []string, extra map[string]string) []string {
	blocked := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_COMMON_DIR": true, "GIT_NAMESPACE": true, "GIT_EXEC_PATH": true}
	result := make([]string, 0, len(existing)+len(extra))
	for _, value := range existing {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] && !strings.HasPrefix(key, "GIT_CONFIG_") {
			if _, replaced := extra[key]; !replaced {
				result = append(result, value)
			}
		}
	}
	for key, value := range extra {
		result = append(result, key+"="+value)
	}
	result = append(result, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return result
}
