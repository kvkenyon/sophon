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
	status, err := g.output(ctx, worktree, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("delivery worktree is not clean")
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

// FetchBranch observes one remote branch into FETCH_HEAD and requires the
// exact caller-recorded SHA. It never updates a local or public branch.
func (g CommandGit) FetchBranch(ctx context.Context, worktree, branch, expectedSHA string) error {
	if strings.TrimSpace(branch) == "" || !fullSHA.MatchString(expectedSHA) {
		return errors.New("public branch and full expected SHA are required")
	}
	if _, err := g.output(ctx, worktree, "fetch", "--no-tags", "origin", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("fetch public branch: %w", err)
	}
	fetched, err := g.output(ctx, worktree, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if !strings.EqualFold(fetched, expectedSHA) {
		return fmt.Errorf("%w: fetched %s, expected %s", ErrHeadMismatch, fetched, expectedSHA)
	}
	return nil
}

// VerifyStrictDescendant proves candidate is a non-equal descendant of base.
func (g CommandGit) VerifyStrictDescendant(ctx context.Context, worktree, baseSHA, candidateSHA string) error {
	if !fullSHA.MatchString(baseSHA) || !fullSHA.MatchString(candidateSHA) {
		return errors.New("full base and candidate SHAs are required")
	}
	if strings.EqualFold(baseSHA, candidateSHA) {
		return errors.New("candidate head is not a strict descendant")
	}
	output, err := exec.CommandContext(ctx, g.binary(), "-C", worktree, "merge-base", "--is-ancestor", baseSHA, candidateSHA).CombinedOutput()
	if err != nil {
		return fmt.Errorf("candidate head is not a descendant of base: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
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

// PushFastForward updates an existing public branch with an ordinary refspec.
// There is deliberately no force or force-with-lease flag: a concurrent or
// non-descendant update is rejected by the remote.
func (r CommandRemote) PushFastForward(ctx context.Context, repository, worktree, branch, baseSHA, headSHA string) error {
	if repository == "" || worktree == "" || branch == "" || !fullSHA.MatchString(baseSHA) || !fullSHA.MatchString(headSHA) {
		return errors.New("repository, worktree, branch, and full base/head SHAs are required")
	}
	actual, err := CommandGit{Binary: r.GitBinary}.Repository(ctx, worktree)
	if err != nil {
		return err
	}
	if actual != repository {
		return errors.New("worktree origin changed after correction preparation")
	}
	if err := (CommandGit{Binary: r.GitBinary}).VerifyStrictDescendant(ctx, worktree, baseSHA, headSHA); err != nil {
		return err
	}
	refspec := strings.ToLower(headSHA) + ":refs/heads/" + branch
	output, err := exec.CommandContext(ctx, r.gitBinary(), "-C", worktree, "push", "origin", refspec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push fast-forward correction: %w: %s", err, strings.TrimSpace(string(output)))
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
	observed, err := r.ObservePullRequest(ctx, repository, number)
	if err != nil {
		return nil, err
	}
	if observed.URL != url || observed.Repository != repository || observed.Branch != branch ||
		!strings.EqualFold(observed.HeadSHA, headSHA) {
		return nil, errors.New("pull request list result does not match canonical observed identity")
	}
	return &observed, nil
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

// DefaultBranch resolves the forge repository's advertised HEAD without
// changing any local ref.
func (r CommandRemote) DefaultBranch(ctx context.Context, repository, worktree string) (string, error) {
	actual, err := CommandGit{Binary: r.GitBinary}.Repository(ctx, worktree)
	if err != nil {
		return "", err
	}
	if actual != repository {
		return "", errors.New("worktree origin does not match the delivery repository")
	}
	output, err := exec.CommandContext(ctx, r.gitBinary(), "-C", worktree, "ls-remote", "--symref", "origin", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect remote default branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" && strings.HasPrefix(fields[1], "refs/heads/") {
			branch := strings.TrimPrefix(fields[1], "refs/heads/")
			if branch != "" {
				return branch, nil
			}
		}
	}
	return "", errors.New("remote did not advertise one default branch")
}

// ObservePullRequest reads canonical PR identity directly through gh-axi's
// API surface. The template contains only fixed scalar identity fields, so
// human-authored title/body content cannot affect parsing.
func (r CommandRemote) ObservePullRequest(ctx context.Context, repository string, number int) (PullRequest, error) {
	host, owner, name, err := splitRepository(repository)
	if err != nil {
		return PullRequest{}, err
	}
	if number < 1 {
		return PullRequest{}, errors.New("positive pull request number is required")
	}
	template := "{{.number}}|{{.html_url}}|{{.state}}|{{.merged}}|{{.head.sha}}|{{.head.ref}}|{{.head.repo.full_name}}|{{.base.ref}}|{{.base.repo.full_name}}"
	output, err := r.gh(ctx, "", "api", fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number), "--template", template)
	if err != nil {
		return PullRequest{}, err
	}
	body, err := parseAPIResponseBody(output)
	if err != nil {
		return PullRequest{}, err
	}
	fields := strings.Split(body, "|")
	if len(fields) != 9 {
		return PullRequest{}, errors.New("gh-axi pull request identity response had the wrong shape")
	}
	observedNumber, err := strconv.Atoi(fields[0])
	if err != nil || observedNumber != number {
		return PullRequest{}, errors.New("gh-axi pull request identity returned a different number")
	}
	state := strings.ToLower(fields[2])
	if strings.EqualFold(fields[3], "true") {
		state = PullRequestMerged
	} else if state != PullRequestOpen && state != PullRequestClosed {
		return PullRequest{}, fmt.Errorf("unknown pull request state %q", fields[2])
	}
	if !fullSHA.MatchString(fields[4]) || fields[5] == "" || fields[6] == "" || fields[7] == "" || fields[8] == "" {
		return PullRequest{}, errors.New("pull request identity is incomplete")
	}
	return PullRequest{
		Repository: host + "/" + fields[6], Branch: fields[5], HeadSHA: strings.ToLower(fields[4]),
		BaseRepository: host + "/" + fields[8], BaseBranch: fields[7], State: state,
		URL: fields[1], Number: number,
	}, nil
}

func (r CommandRemote) gh(ctx context.Context, worktree string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, r.ghBinary(), args...)
	if worktree != "" {
		command.Dir = worktree
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh-axi %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func splitRepository(repository string) (host, owner, name string, err error) {
	parts := strings.Split(strings.Trim(repository, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("invalid canonical repository %q", repository)
	}
	return parts[0], parts[1], parts[2], nil
}

func parseAPIResponseBody(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "body:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "body:"))
		if raw == "" {
			return "", errors.New("gh-axi API response body was empty")
		}
		if strings.HasPrefix(raw, "\"") {
			decoded, err := strconv.Unquote(raw)
			if err != nil {
				return "", fmt.Errorf("decode gh-axi API response body: %w", err)
			}
			return decoded, nil
		}
		return raw, nil
	}
	return "", errors.New("gh-axi API response omitted body")
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
