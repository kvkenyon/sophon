package commander

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
)

type commanderLabRunner struct {
	helper  string
	session string
}

func (r commanderLabRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if len(args) < 2 || args[len(args)-2] != "--session" || args[len(args)-1] != r.session {
		return nil, nil, errors.New("commander adapter omitted explicit lab session")
	}
	command := exec.CommandContext(ctx, r.helper, append([]string{"run", r.session}, args[:len(args)-2]...)...)
	stdout, err := command.Output()
	var stderr []byte
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr = exitErr.Stderr
	}
	return stdout, stderr, err
}

func TestRealHerdrCodexCommanderSmoke(t *testing.T) {
	if os.Getenv("PARALLEL_INTELLECT_COMMANDER_SMOKE") != "1" {
		t.Skip("set PARALLEL_INTELLECT_COMMANDER_SMOKE=1 to launch a commander in an isolated Herdr lab")
	}
	helper := os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		helper = "/Users/kevin/github/kvkenyon/research/firstmate/bin/fm-herdr-lab.sh"
	}
	sessionName := strings.TrimSpace(os.Getenv("HERDR_LAB_SESSION"))
	if sessionName == "" {
		output, err := exec.Command(helper, "name", "parallel-intellect-m7-commander-runtimes").Output()
		if err != nil {
			t.Fatal(err)
		}
		sessionName = strings.TrimSpace(string(output))
	}
	if !strings.HasPrefix(sessionName, "fm-lab-") || sessionName == "default" {
		t.Fatalf("unsafe Herdr lab session %q", sessionName)
	}
	if os.Getenv("HERDR_LAB_PROVISIONED") != "1" {
		attempted := false
		t.Cleanup(func() {
			if attempted {
				if output, err := exec.Command(helper, "teardown", sessionName).CombinedOutput(); err != nil {
					t.Errorf("teardown commander Herdr lab: %v: %s", err, output)
				}
			}
		})
		attempted = true
		if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
			t.Fatalf("provision commander Herdr lab: %v: %s", err, output)
		}
	}

	projectPath, err := os.MkdirTemp(".", ".herdr-m7-commander-")
	if err != nil {
		t.Fatal(err)
	}
	projectPath, err = filepath.Abs(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(projectPath) })
	promptDir, err := filepath.Abs(filepath.Join("..", "..", "prompts", "commander"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectID, err := store.CreateProject(context.Background(), "cmd_m7_live_project", db.CreateProjectInput{
		Name: "m7-live-commander", Path: projectPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(context.Background(), "cmd_m7_live_mission", db.CreateMissionInput{
		ProjectID: projectID, Title: "M7 live commander proof",
		Objective:          "For this isolated runtime proof, reply exactly COMMANDER_M7_CONTEXT_OK and then wait for another message.",
		AcceptanceCriteria: []domain.Criterion{{Description: "A later steer receives the exact response COMMANDER_M7_STEER_OK."}},
		Budget:             domain.MissionBudget{MaxTaskAttempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := commanderLabRunner{helper: helper, session: sessionName}
	terminal := herdr.NewCommandAdapterWithRunner(sessionName, "pi-m7-commander", runner)
	runtime := HerdrAdapter{Terminal: terminal}
	started, err := (&Starter{Store: store, Runtime: runtime, Prompts: PromptComposer{Dir: promptDir}}).Start(
		context.Background(), StartRequest{MissionID: mission.ID, Runtime: herdr.RuntimeCodex})
	if err != nil {
		t.Fatal(err)
	}
	waitCommanderState(t, runtime, runtimeSession(started.Session, projectPath), StateIdle, 4*time.Minute)
	waitCommanderPaneText(t, runner, sessionName, started.Session.HerdrPaneID, "COMMANDER_M7_CONTEXT_OK", time.Minute)

	if _, err := (&Controller{Store: store, Runtime: runtime}).Send(context.Background(), mission.ID, MessageSteer,
		"Steer proof: reply exactly COMMANDER_M7_STEER_OK and then wait."); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.CommanderSession(context.Background(), mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitCommanderState(t, runtime, runtimeSession(persisted, projectPath), StateIdle, 4*time.Minute)
	waitCommanderPaneText(t, runner, sessionName, persisted.HerdrPaneID, "COMMANDER_M7_STEER_OK", time.Minute)
	t.Logf("live Codex commander received mission context and responded to a subsequent steer in %s", sessionName)
}

func waitCommanderState(t *testing.T, runtime Adapter, session Session, want State, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := runtime.State(ctx, session)
		if err == nil && state == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("commander did not reach %s: last=%s error=%v", want, state, err)
		case <-ticker.C:
		}
	}
}

func waitCommanderPaneText(t *testing.T, runner commanderLabRunner, sessionName, paneID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		stdout, stderr, err := runner.Run(context.Background(), "pane", "read", paneID,
			"--source", "recent", "--lines", "200", "--session", sessionName)
		if err == nil && strings.Contains(string(stdout), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("commander pane omitted %q: error=%v stderr=%s pane=%s", want, err, stderr, stdout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
