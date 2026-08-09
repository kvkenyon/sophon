package prompts

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// requiredCommanderClauses are the behavioral contracts the rendered
// commander prompt must always carry.
var requiredCommanderClauses = []string{
	"sole point of contact",
	"restart is a non-event",
	"outcomes, not mechanics",
	"notifications, never state",
	"sophon commander attach",
	"--confirmed",
	"operator confirmation",
	"action queue first",
	"Drain the action queue to a fixed point",
	"ready for my verification",
}

// requiredWorkerClauses are the behavioral contracts every worker brief must
// carry through the common and implementation overlays.
var requiredWorkerClauses = []string{
	"isolated attempt worktree",
	"sophon worker complete",
	"--head-sha",
	"--result",
	"Never mutate shared state",
	"Escalate only concrete decisions and blockers",
}

// forbiddenBranding must never appear in any shipped prompt or rendered
// output, in any case.
var forbiddenBranding = []string{
	"firstmate", "first mate", "captain", "crewmate",
	"pintellect", "parallel-intellect",
}

func TestCommanderPromptCarriesBehavioralContracts(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	body, err := Compose(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	for _, clause := range requiredCommanderClauses {
		if !strings.Contains(body, clause) {
			t.Errorf("rendered commander prompt omitted %q", clause)
		}
	}
}

// The drain procedure must run every verify-complete before any validate,
// and reporting comes only after the queue is empty.
func TestCommanderPromptOrdersDrainBeforeReporting(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	body, err := Compose(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	drain := strings.Index(body, "Drain the action queue to a fixed point")
	if drain < 0 {
		t.Fatal("rendered commander prompt omitted the drain procedure")
	}
	rest := body[drain:]
	verify := strings.Index(rest, "sophon verify-complete")
	validate := strings.Index(rest, "sophon validate")
	report := strings.Index(rest, "Only then")
	if verify < 0 || validate < 0 || report < 0 || verify > validate || validate > report {
		t.Errorf("drain ordering broken: verify at %d, validate at %d, report at %d", verify, validate, report)
	}
}

func TestWorkerOverlaysCarryBehavioralContracts(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	promptFS, root, err := Set("workers")
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for _, name := range []string{"common.md", "implementation.md"} {
		content, err := fs.ReadFile(promptFS, root+"/"+name)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(content)
		body.WriteString("\n")
	}
	for _, clause := range requiredWorkerClauses {
		if !strings.Contains(body.String(), clause) {
			t.Errorf("worker overlays omitted %q", clause)
		}
	}
}

func TestPromptTreeHasNoForbiddenBranding(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	err := fs.WalkDir(Embedded, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(Embedded, path)
		if err != nil {
			return err
		}
		assertNoForbiddenBranding(t, path, string(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRenderedPromptsHaveNoForbiddenBranding(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	commander, err := Compose(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoForbiddenBranding(t, "rendered commander prompt", commander)
}

func assertNoForbiddenBranding(t *testing.T, name, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, token := range forbiddenBranding {
		if strings.Contains(lower, token) {
			t.Errorf("%s contains forbidden branding %q", name, token)
		}
	}
}
