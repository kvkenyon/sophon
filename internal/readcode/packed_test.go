package readcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPackedProductContract is opt-in because Read the Code is deliberately
// not an installed/published dependency. Release validation packs the product
// and points this test at that exact extracted executable.
func TestPackedProductContract(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("SOPHON_READ_CODE_INTEGRATION_BIN"))
	if binary == "" {
		t.Skip("set SOPHON_READ_CODE_INTEGRATION_BIN to an explicitly packed Read the Code executable")
	}
	state := t.TempDir()
	t.Setenv("READ_THE_CODE_STATE_DIR", state)
	repo := t.TempDir()
	runPackedGit(t, repo, "init", "-b", "main")
	runPackedGit(t, repo, "config", "user.name", "Sophon integration")
	runPackedGit(t, repo, "config", "user.email", "integration@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPackedGit(t, repo, "add", "README.md")
	runPackedGit(t, repo, "commit", "-m", "base")
	base := runPackedGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\nchange\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPackedGit(t, repo, "add", "README.md")
	runPackedGit(t, repo, "commit", "-m", "change")
	head := runPackedGit(t, repo, "rev-parse", "HEAD")

	client := Client{Binary: binary}
	opened, err := client.Open(context.Background(), OpenRequest{Repository: repo, BaseSHA: base, HeadSHA: head, NoBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background(), opened.SessionID)
	if err != nil || status.BaseSHA != base || status.HeadSHA != head || status.LastSequence != 0 {
		t.Fatalf("packed status = %+v, %v", status, err)
	}
	polled, err := client.Poll(context.Background(), opened.SessionID, 0, 20*time.Millisecond)
	if err != nil || !polled.TimedOut || polled.NextCursor != 0 {
		t.Fatalf("packed poll = %+v, %v", polled, err)
	}
	ended, err := client.End(context.Background(), opened.SessionID)
	if err != nil || ended.Event.Type != "end" || ended.Event.Sequence != 1 {
		t.Fatalf("packed end = %+v, %v", ended, err)
	}
}

func runPackedGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
