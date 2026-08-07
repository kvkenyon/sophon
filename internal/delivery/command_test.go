package delivery

import "testing"

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
