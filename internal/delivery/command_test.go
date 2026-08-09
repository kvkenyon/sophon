package delivery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRepositoryStripsCredentialsAndTransport(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":               "github.com/owner/repo",
		"https://token@github.com/owner/repo.git":     "github.com/owner/repo",
		"ssh://git@github.example.com/owner/repo.git": "github.example.com/owner/repo",
	}
	for remote, want := range cases {
		got, err := normalizeRepository(remote)
		if err != nil {
			t.Fatalf("normalize %q: %v", remote, err)
		}
		if got != want {
			t.Fatalf("normalize %q = %q, want %q", remote, got, want)
		}
	}
}

func TestParseGHAxiPullRequestOutput(t *testing.T) {
	list := `count: 1
pull_requests[1]{number,title,state,author,draft,review,url}:
  17,"Delivery task",open,octocat,no,none,"https://github.com/owner/repo/pull/17"
`
	number, url, found, err := parsePRList(list)
	if err != nil || !found || number != 17 || url != "https://github.com/owner/repo/pull/17" {
		t.Fatalf("parsed list number=%d url=%q found=%v err=%v", number, url, found, err)
	}
	number, url, err = parseCreatedPR(`created:
  number: 17
  url: "https://github.com/owner/repo/pull/17"
`)
	if err != nil || number != 17 || url != "https://github.com/owner/repo/pull/17" {
		t.Fatalf("parsed create number=%d url=%q err=%v", number, url, err)
	}
	if _, _, found, err := parsePRList("count: 0\npull_requests[0]:\n"); err != nil || found {
		t.Fatalf("empty list found=%v err=%v", found, err)
	}
}

func TestParseGHAxiAPIIdentityEnvelope(t *testing.T) {
	body, err := parseAPIResponseBody(`api_response:
  body: "6818|https://github.com/light-technology/backend/pull/6818|open|false|cfa1ef810d95f2a091b895f4fe21d8a0191cd3f5|feature/client|light-technology/backend|develop|light-technology/backend"
  truncated: false
`)
	if err != nil || !strings.HasPrefix(body, "6818|") {
		t.Fatalf("body = %q, err = %v", body, err)
	}
	host, owner, repo, err := splitRepository("github.com/light-technology/backend")
	if err != nil || host != "github.com" || owner != "light-technology" || repo != "backend" {
		t.Fatalf("repository = %s/%s/%s, err = %v", host, owner, repo, err)
	}
}

func TestPushFastForwardRejectsConcurrentNonFastForwardWithoutForce(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	external := filepath.Join(root, "external")
	gitRun(t, realGit, "", "init", "--bare", origin)
	gitRun(t, realGit, "", "init", "-b", "main", work)
	gitRun(t, realGit, work, "config", "user.name", "Sophon Test")
	gitRun(t, realGit, work, "config", "user.email", "sophon@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, realGit, work, "add", "base.txt")
	gitRun(t, realGit, work, "commit", "-m", "base")
	base := gitRun(t, realGit, work, "rev-parse", "HEAD")
	gitRun(t, realGit, work, "remote", "add", "origin", origin)
	gitRun(t, realGit, work, "push", "origin", "main")
	gitRun(t, realGit, work, "push", "origin", base+":refs/heads/review/change")

	gitRun(t, realGit, work, "switch", "-c", "private-correction", base)
	if err := os.WriteFile(filepath.Join(work, "correction.txt"), []byte("correction\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, realGit, work, "add", "correction.txt")
	gitRun(t, realGit, work, "commit", "-m", "bounded correction")
	candidate := gitRun(t, realGit, work, "rev-parse", "HEAD")

	gitRun(t, realGit, "", "clone", origin, external)
	gitRun(t, realGit, external, "config", "user.name", "External Test")
	gitRun(t, realGit, external, "config", "user.email", "external@example.invalid")
	gitRun(t, realGit, external, "switch", "-c", "review-change", "origin/review/change")
	if err := os.WriteFile(filepath.Join(external, "external.txt"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, realGit, external, "add", "external.txt")
	gitRun(t, realGit, external, "commit", "-m", "unexpected external commit")
	gitRun(t, realGit, external, "push", "origin", "HEAD:refs/heads/review/change")
	externalHead := gitRun(t, realGit, external, "rev-parse", "HEAD")

	wrapper := filepath.Join(root, "git-wrapper")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "-C" ] && [ "$3" = "remote" ] && [ "$4" = "get-url" ] && [ "$5" = "origin" ]; then
  printf 'git@github.com:acme/repo.git\n'
  exit 0
fi
exec %s "$@"
`, realGit)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	remote := CommandRemote{GitBinary: wrapper}
	if err := remote.PushFastForward(context.Background(), "github.com/acme/repo", work,
		"review/change", base, candidate); err == nil || !strings.Contains(err.Error(), "push fast-forward") {
		t.Fatalf("non-fast-forward correction push error = %v", err)
	}
	if got := gitRun(t, realGit, origin, "rev-parse", "refs/heads/review/change"); got != externalHead {
		t.Fatalf("rejected push changed remote head to %s, want %s", got, externalHead)
	}
}

func gitRun(t *testing.T, binary, dir string, args ...string) string {
	t.Helper()
	command := exec.Command(binary, args...)
	if dir != "" {
		command.Dir = dir
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
