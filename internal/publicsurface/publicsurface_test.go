package publicsurface

import (
	"strings"
	"testing"

	"sophon/internal/domain"
)

func TestTaskTitleAndBranchContract(t *testing.T) {
	if err := TaskTitle("HOME-111 Add Tesla fleet client"); err != nil {
		t.Fatal(err)
	}
	if err := Branch("home-111/tesla-fleet-client"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		value string
		title bool
	}{
		{"multiline title", "A title\nwith detail", true},
		{"control title", "A title\x00", true},
		{"long title", strings.Repeat("x", MaxTitleLength+1), true},
		{"invalid ref", "feature/name..lock", false},
		{"path-like ref", "/Users/alice/project", false},
		{"branded ref", "sophon/feature", false},
		{"attempt ref", "feature/attempt-1", false},
		{"internal id ref", "feature/task_f0bbc2200213c81f3b03223fb4dc454c", false},
		{"local link", "See http://localhost:4387/session/abc", true},
		{"orchestrator", "Firstmate generated change", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.title {
				err = TaskTitle(test.value)
			} else {
				err = Branch(test.value)
			}
			if err == nil {
				t.Fatalf("accepted %q", test.value)
			}
		})
	}
}

func TestPreflightRejectsOriginalPublicLeak(t *testing.T) {
	giantTitle := "Implement the full Tesla Fleet API BaseClient behavior with every private orchestration instruction copied into the public pull request title"
	oldBody := "Sophon task task_f0bbc2200213c81f3b03223fb4dc454c attempt 1"
	oldBranch := "sophon/home-111-tesla-fleet-api-baseclient-taskf0bb/attempt-1"
	for name, mutate := range map[string]func() (string, string, string, []string){
		"title": func() (string, string, string, []string) {
			return "feature/clean", strings.Repeat(giantTitle, 2), "Useful body", []string{"Add client"}
		},
		"body": func() (string, string, string, []string) {
			return "feature/clean", "Add client", oldBody, []string{"Add client"}
		},
		"branch": func() (string, string, string, []string) {
			return oldBranch, "Add client", "Useful body", []string{"Add client"}
		},
		"commit": func() (string, string, string, []string) {
			return "feature/clean", "Add client", "Useful body", []string{"Complete task_f0bbc2200213c81f3b03223fb4dc454c"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			branch, title, body, commits := mutate()
			if err := Preflight(branch, title, body, commits); err == nil {
				t.Fatal("preflight accepted private public value")
			}
		})
	}
}

func TestPullRequestBodyCuratesPrivateEvidence(t *testing.T) {
	result := domain.WorkerResult{
		Summary: "Implemented the fleet client through Sophon task task_f0bbc2200213c81f3b03223fb4dc454c",
		Verification: []domain.VerificationResult{
			{Command: "SOPHON_DATA_HOME=/Users/alice/.sophon go test ./...", ExitCode: 0},
			{Command: "cd /Users/alice/.treehouse/private && go vet ./...", ExitCode: 0},
		},
		ChangedFiles: []string{"client.go", ".sophon/result.json"},
		Risks:        []string{"Herdr pane session may be stale", "Retries remain caller-managed"},
	}
	body := PullRequestBody("HOME-111 Add Tesla fleet client", result)
	for _, want := range []string{"## Summary", "HOME-111 Add Tesla fleet client", "Updated `client.go`", "`go test ./...` (passed)", "`go vet ./...` (passed)", "Retries remain caller-managed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	for _, private := range []string{"sophon", "task_f0", "/Users/", ".treehouse", "Herdr", "pane session"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(private)) {
			t.Fatalf("body leaked %q:\n%s", private, body)
		}
	}
	if err := Preflight("home-111/tesla-fleet-client", "HOME-111 Add Tesla fleet client", body, []string{"HOME-111 Add Tesla fleet client"}); err != nil {
		t.Fatal(err)
	}
}

func TestGenericProductLanguageIsNotBlindlyRejected(t *testing.T) {
	value := "Improve retry attempts for background task workers"
	if err := Validate("product prose", value); err != nil {
		t.Fatalf("ordinary product prose rejected: %v", err)
	}
}
