package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type runnerResponse struct {
	stdout string
	stderr string
	err    error
}

type fakeRunner struct {
	responses []runnerResponse
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(f.responses) == 0 {
		return nil, nil, errors.New("unexpected Herdr call")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return []byte(response.stdout), []byte(response.stderr), response.err
}

func TestCommandAdapterStartsCodexAndDeliversBriefAsInitialPrompt(t *testing.T) {
	runner := &fakeRunner{responses: []runnerResponse{
		{stdout: `{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"result":{"type":"agent_started"}}`},
		{stdout: `{"result":{"type":"prompt_sent"}}`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "Parallel Intellect", runner)
	session, err := adapter.StartCodex(context.Background(), StartRequest{
		TaskID: "tsk_contract", Attempt: 2, WorktreePath: "/worktrees/task", Brief: "complete generated brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionName != "fm-lab-contract" || session.WorkspaceID != "w1" ||
		session.TabID != "w1:t1" || session.PaneID != "w1:p1" {
		t.Fatalf("session = %+v", session)
	}
	want := [][]string{
		{"workspace", "create", "--cwd", "/worktrees/task", "--label", "Parallel Intellect", "--no-focus", "--session", "fm-lab-contract"},
		{"agent", "start", "pi-tsk_contract-a2", "--kind", "codex", "--pane", "w1:p1", "--session", "fm-lab-contract"},
		{"agent", "prompt", "w1:p1", "complete generated brief", "--session", "fm-lab-contract"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, want)
	}
}

func TestCommandAdapterObservesRunningIdleAndLost(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		runErr error
		want   State
	}{
		{name: "running", body: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"working"}}}`, want: StateRunning},
		{name: "idle", body: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle"}}}`, want: StateIdle},
		{name: "done is idle", body: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"done"}}}`, want: StateIdle},
		{name: "blocked is idle", body: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"blocked"}}}`, want: StateIdle},
		{name: "lost agent", body: `{"error":{"code":"agent_not_found"}}`, runErr: errors.New("exit 1"), want: StateLost},
		{name: "lost pane", body: `{"error":{"code":"pane_not_found"}}`, runErr: errors.New("exit 1"), want: StateLost},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []runnerResponse{{stdout: test.body, err: test.runErr}}}
			adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
			state, err := adapter.Observe(context.Background(), Session{PaneID: "w1:p1"})
			if err != nil {
				t.Fatal(err)
			}
			if state != test.want {
				t.Fatalf("state = %s, want %s", state, test.want)
			}
			call := runner.calls[0]
			if got := call[len(call)-2:]; !reflect.DeepEqual(got, []string{"--session", "fm-lab-contract"}) {
				t.Fatalf("Herdr session routing = %#v", call)
			}
		})
	}
}

type labRunner struct {
	helper  string
	session string
}

func (r labRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if len(args) < 2 || args[len(args)-2] != "--session" || args[len(args)-1] != r.session {
		return nil, nil, errors.New("adapter omitted explicit lab session")
	}
	helperArgs := append([]string{"run", r.session}, args[:len(args)-2]...)
	command := exec.CommandContext(ctx, r.helper, helperArgs...)
	stdout, err := command.Output()
	var stderr []byte
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr = exitErr.Stderr
	}
	return stdout, stderr, err
}

func TestRealHerdrObservationSmoke(t *testing.T) {
	if os.Getenv("PARALLEL_INTELLECT_HERDR_SMOKE") != "1" {
		t.Skip("set PARALLEL_INTELLECT_HERDR_SMOKE=1 to exercise Herdr in an isolated lab session")
	}
	helper := os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		helper = "/Users/kevin/github/kvkenyon/research/firstmate/bin/fm-herdr-lab.sh"
	}
	nameOutput, err := exec.Command(helper, "name", "parallel-intellect-m3-vertical-slice").Output()
	if err != nil {
		t.Fatal(err)
	}
	sessionName := strings.TrimSpace(string(nameOutput))
	if !strings.HasPrefix(sessionName, "fm-lab-") || sessionName == "default" {
		t.Fatalf("unsafe Herdr lab session %q", sessionName)
	}
	if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("provision Herdr lab: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command(helper, "teardown", sessionName).CombinedOutput(); err != nil {
			t.Errorf("teardown Herdr lab: %v: %s", err, output)
		}
	})
	runner := labRunner{helper: helper, session: sessionName}
	stdout, stderr, err := runner.Run(context.Background(), "workspace", "create", "--cwd", t.TempDir(),
		"--label", "pi-m3-observe", "--no-focus", "--session", sessionName)
	if err != nil {
		t.Fatalf("create smoke workspace: %v: %s", err, stderr)
	}
	var created struct {
		Result struct {
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout, &created); err != nil || created.Result.RootPane.ID == "" {
		t.Fatalf("decode smoke workspace: %v: %s", err, stdout)
	}
	pane := created.Result.RootPane.ID
	if _, stderr, err := runner.Run(context.Background(), "pane", "report-agent", pane, "--source", "pi-m3-smoke",
		"--agent", "codex", "--state", "working", "--session", sessionName); err != nil {
		t.Fatalf("report synthetic smoke agent: %v: %s", err, stderr)
	}
	adapter := NewCommandAdapterWithRunner(sessionName, "", runner)
	state, err := adapter.Observe(context.Background(), Session{SessionName: sessionName, PaneID: pane})
	if err != nil || state != StateRunning {
		t.Fatalf("smoke observation = %s, %v", state, err)
	}
}
