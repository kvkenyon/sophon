// Package git provides the immutable-SHA and clean-worktree checks used to
// fence worker completion from the task control plane.
package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var (
	ErrInvalidSHA    = errors.New("invalid Git SHA")
	ErrNoNewCommit   = errors.New("HEAD does not contain a new commit")
	ErrNotDescendant = errors.New("HEAD does not descend from base SHA")
	ErrDirtyTree     = errors.New("Git worktree is not clean")
)

var fullSHA = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

type Snapshot struct {
	Head   string
	Branch string
	Clean  bool
}

type Completion struct {
	BaseSHA string
	HeadSHA string
	Branch  string
}

type Client struct {
	Binary string
}

func NewClient() *Client { return &Client{Binary: "git"} }

// CreateTaskBranch records the detached pool worktree's base commit, verifies
// it is clean, then attaches HEAD to branch. Treehouse pool worktrees are
// intentionally detached, so branch resolution must happen only after this
// operation.
func (c *Client) CreateTaskBranch(ctx context.Context, worktreePath, branch string) (Snapshot, error) {
	head, err := c.output(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve worktree HEAD: %w", err)
	}
	if !fullSHA.MatchString(head) {
		return Snapshot{}, fmt.Errorf("%w: %q", ErrInvalidSHA, head)
	}
	status, err := c.output(ctx, worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect worktree cleanliness: %w", err)
	}
	if status != "" {
		return Snapshot{Head: head, Clean: false}, nil
	}
	if branch == "" {
		return Snapshot{}, errors.New("task branch is required")
	}
	if _, err := c.output(ctx, worktreePath, "switch", "-c", branch, head); err != nil {
		return Snapshot{}, fmt.Errorf("create task branch %q: %w", branch, err)
	}
	return c.Snapshot(ctx, worktreePath)
}

// Snapshot records the full commit identity and branch at acquisition time.
func (c *Client) Snapshot(ctx context.Context, worktreePath string) (Snapshot, error) {
	head, err := c.output(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve worktree HEAD: %w", err)
	}
	if !fullSHA.MatchString(head) {
		return Snapshot{}, fmt.Errorf("%w: %q", ErrInvalidSHA, head)
	}
	branch, err := c.output(ctx, worktreePath, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve worktree branch: %w", err)
	}
	status, err := c.output(ctx, worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect worktree cleanliness: %w", err)
	}
	return Snapshot{Head: head, Branch: branch, Clean: status == ""}, nil
}

// VerifyCompletion proves that HEAD is a distinct descendant of the recorded
// base commit and that tracked and untracked worktree changes are absent.
func (c *Client) VerifyCompletion(ctx context.Context, worktreePath, baseSHA string) (Completion, error) {
	if !fullSHA.MatchString(baseSHA) {
		return Completion{}, fmt.Errorf("%w: %q", ErrInvalidSHA, baseSHA)
	}
	snapshot, err := c.Snapshot(ctx, worktreePath)
	if err != nil {
		return Completion{}, err
	}
	if snapshot.Head == strings.ToLower(baseSHA) || strings.EqualFold(snapshot.Head, baseSHA) {
		return Completion{}, ErrNoNewCommit
	}
	command := exec.CommandContext(ctx, c.binary(), "-C", worktreePath,
		"merge-base", "--is-ancestor", baseSHA, snapshot.Head)
	if output, err := command.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return Completion{}, ErrNotDescendant
		}
		return Completion{}, fmt.Errorf("verify HEAD ancestry: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if !snapshot.Clean {
		return Completion{}, ErrDirtyTree
	}
	return Completion{BaseSHA: baseSHA, HeadSHA: snapshot.Head, Branch: snapshot.Branch}, nil
}

func (c *Client) output(ctx context.Context, path string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", path}, args...)
	output, err := exec.CommandContext(ctx, c.binary(), commandArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func (c *Client) binary() string {
	if c.Binary == "" {
		return "git"
	}
	return c.Binary
}
