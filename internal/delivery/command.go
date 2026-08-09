package delivery

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var fullSHA = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// CommandGit provides the local immutable-head checks used immediately before
// every delivery phase.
type CommandGit struct {
	Binary string
}

func (g CommandGit) VerifyHead(ctx context.Context, worktree, branch, headSHA string) error {
	if !fullSHA.MatchString(headSHA) {
		return fmt.Errorf("%w: invalid recorded SHA %q", ErrHeadMismatch, headSHA)
	}
	head, err := g.output(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if !strings.EqualFold(head, headSHA) {
		return fmt.Errorf("%w: worktree %s, attempt %s", ErrHeadMismatch, head, headSHA)
	}
	currentBranch, err := g.output(ctx, worktree, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if currentBranch != branch {
		return fmt.Errorf("%w: worktree %q, attempt %q", ErrBranchMismatch, currentBranch, branch)
	}
	return nil
}

func (g CommandGit) Repository(ctx context.Context, worktree string) (string, error) {
	repository, err := g.output(ctx, worktree, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("resolve delivery repository: %w", err)
	}
	if strings.TrimSpace(repository) == "" {
		return "", errors.New("origin remote has no repository URL")
	}
	normalized, err := normalizeRepository(repository)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

// CommitMessages returns every commit message that will become public when
// the verified head is pushed. The base is excluded and the head included.
func (g CommandGit) CommitMessages(ctx context.Context, worktree, baseSHA, headSHA string) ([]string, error) {
	if !fullSHA.MatchString(baseSHA) || !fullSHA.MatchString(headSHA) {
		return nil, errors.New("full base and head SHAs are required to inspect commit messages")
	}
	output, err := exec.CommandContext(ctx, g.binary(), "-C", worktree, "log", "--format=%B%x00", baseSHA+".."+headSHA).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("inspect delivery commit messages: %w: %s", err, strings.TrimSpace(string(output)))
	}
	parts := bytes.Split(output, []byte{0})
	messages := make([]string, 0, len(parts))
	for _, part := range parts {
		message := strings.TrimSpace(string(part))
		if message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		return nil, errors.New("verified delivery contains no public commits")
	}
	return messages, nil
}

func (g CommandGit) output(ctx context.Context, worktree string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", worktree}, args...)
	output, err := exec.CommandContext(ctx, g.binary(), commandArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func (g CommandGit) binary() string {
	if g.Binary == "" {
		return "git"
	}
	return g.Binary
}

// CommandRemote keeps all outward Git/GitHub work behind the delivery Remote
// boundary. gh-axi runs in the attempt worktree so its repository context is
// the same repository identity persisted with the delivery intent.
type CommandRemote struct {
	GitBinary string
	GHBinary  string
}

func (r CommandRemote) Push(ctx context.Context, repository, worktree, branch, headSHA string) error {
	if repository == "" || worktree == "" || branch == "" || !fullSHA.MatchString(headSHA) {
		return errors.New("repository, worktree, branch, and full verified SHA are required")
	}
	actual, err := CommandGit{Binary: r.GitBinary}.Repository(ctx, worktree)
	if err != nil {
		return err
	}
	if actual != repository {
		return errors.New("worktree origin changed after delivery preparation")
	}
	refspec := strings.ToLower(headSHA) + ":refs/heads/" + branch
	lease := "--force-with-lease=refs/heads/" + branch + ":"
	output, err := exec.CommandContext(ctx, r.gitBinary(), "-C", worktree, "push", lease, "origin", refspec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push exact SHA: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r CommandRemote) FindPullRequest(ctx context.Context, repository, worktree, branch, headSHA string) (*PullRequest, error) {
	remoteHead, err := r.HeadSHA(ctx, repository, worktree, branch)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(remoteHead, headSHA) {
		return nil, nil
	}
	output, err := r.gh(ctx, worktree, "pr", "list", "--state", "all", "--head", branch,
		"--limit", "10", "--fields", "number,url")
	if err != nil {
		return nil, err
	}
	number, url, found, err := parsePRList(output)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &PullRequest{Repository: repository, Branch: branch, HeadSHA: strings.ToLower(headSHA), URL: url, Number: number}, nil
}

func (r CommandRemote) CreatePullRequest(ctx context.Context, in PullRequestInput) (PullRequest, error) {
	args := []string{"pr", "create", "--title", in.Title, "--body", in.Body, "--head", in.Branch}
	if in.Base != "" {
		args = append(args, "--base", in.Base)
	}
	output, err := r.gh(ctx, in.Worktree, args...)
	if err != nil {
		if existing, reconcileErr := r.FindPullRequest(ctx, in.Repository, in.Worktree, in.Branch, in.HeadSHA); reconcileErr == nil && existing != nil {
			return *existing, nil
		}
		return PullRequest{}, err
	}
	number, url, err := parseCreatedPR(output)
	if err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Repository: in.Repository, Branch: in.Branch, HeadSHA: strings.ToLower(in.HeadSHA), URL: url, Number: number}, nil
}

func (r CommandRemote) HeadSHA(ctx context.Context, repository, worktree, branch string) (string, error) {
	head, exists, err := r.BranchHead(ctx, repository, worktree, branch)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("remote branch %q does not exist", branch)
	}
	return head, nil
}

// BranchHead observes an explicit public branch without mutating it.
func (r CommandRemote) BranchHead(ctx context.Context, repository, worktree, branch string) (string, bool, error) {
	actual, err := CommandGit{Binary: r.GitBinary}.Repository(ctx, worktree)
	if err != nil {
		return "", false, err
	}
	if actual != repository {
		return "", false, errors.New("worktree origin does not match the prepared delivery repository")
	}
	output, err := exec.CommandContext(ctx, r.gitBinary(), "-C", worktree, "ls-remote", "--heads", "origin",
		"refs/heads/"+branch).CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("inspect remote branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", false, nil
	}
	if len(fields) != 2 || !fullSHA.MatchString(fields[0]) {
		return "", false, fmt.Errorf("remote branch %q did not resolve to one full SHA", branch)
	}
	return strings.ToLower(fields[0]), true, nil
}

func (r CommandRemote) gh(ctx context.Context, worktree string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, r.ghBinary(), args...)
	command.Dir = worktree
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh-axi %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (r CommandRemote) gitBinary() string {
	if r.GitBinary == "" {
		return "git"
	}
	return r.GitBinary
}

func (r CommandRemote) ghBinary() string {
	if r.GHBinary == "" {
		return "gh-axi"
	}
	return r.GHBinary
}

func parsePRList(output string) (int, string, bool, error) {
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "pull_requests[") || !strings.Contains(trimmed, "]{") {
			continue
		}
		open := strings.Index(trimmed, "{")
		close := strings.Index(trimmed, "}")
		if open < 0 || close <= open {
			return 0, "", false, errors.New("decode gh-axi pull request columns")
		}
		columns := strings.Split(trimmed[open+1:close], ",")
		if index+1 >= len(lines) || strings.TrimSpace(lines[index+1]) == "" {
			return 0, "", false, nil
		}
		values, err := csv.NewReader(strings.NewReader(strings.TrimSpace(lines[index+1]))).Read()
		if err != nil || len(values) != len(columns) {
			return 0, "", false, errors.New("decode gh-axi pull request row")
		}
		var numberRaw, url string
		for i, column := range columns {
			switch column {
			case "number":
				numberRaw = values[i]
			case "url":
				url = values[i]
			}
		}
		number, err := strconv.Atoi(numberRaw)
		if err != nil || number < 1 || strings.TrimSpace(url) == "" {
			return 0, "", false, errors.New("gh-axi pull request result omitted number or URL")
		}
		return number, url, true, nil
	}
	if strings.Contains(output, "count: 0") || strings.Contains(output, "pull_requests[0]") {
		return 0, "", false, nil
	}
	return 0, "", false, errors.New("unrecognized gh-axi pull request list output")
}

func parseCreatedPR(output string) (int, string, error) {
	numberPattern := regexp.MustCompile(`(?m)^\s*number:\s*(\d+)\s*$`)
	urlPattern := regexp.MustCompile(`(?m)^\s*url:\s*"?([^"\s]+)"?\s*$`)
	numberMatch := numberPattern.FindStringSubmatch(output)
	urlMatch := urlPattern.FindStringSubmatch(output)
	if len(numberMatch) != 2 || len(urlMatch) != 2 {
		return 0, "", errors.New("decode gh-axi created pull request")
	}
	number, err := strconv.Atoi(numberMatch[1])
	if err != nil || number < 1 {
		return 0, "", errors.New("invalid created pull request number")
	}
	return number, urlMatch[1], nil
}

func normalizeRepository(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", errors.New("empty Git repository remote")
	}
	if !strings.Contains(remote, "://") {
		if colon := strings.Index(remote, ":"); colon > 0 {
			host := remote[:colon]
			if at := strings.LastIndex(host, "@"); at >= 0 {
				host = host[at+1:]
			}
			path := strings.TrimSuffix(strings.Trim(remote[colon+1:], "/"), ".git")
			if host != "" && strings.Count(path, "/") == 1 {
				return host + "/" + path, nil
			}
		}
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("parse Git repository remote %q", remote)
	}
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if strings.Count(path, "/") != 1 {
		return "", fmt.Errorf("Git repository remote %q is not an owner/repository path", remote)
	}
	return parsed.Hostname() + "/" + path, nil
}
