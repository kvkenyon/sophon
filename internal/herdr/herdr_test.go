package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
		{stdout: `{"result":{"type":"command_started"}}`},
		{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle","state_change_seq":1}}}`},
		{stdout: `OpenAI Codex`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_session":{"value":"codex-session-2"}},"type":"prompt_sent"}}`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "Parallel Intellect", runner)
	session, err := adapter.StartCodex(context.Background(), StartRequest{
		TaskID: "tsk_contract", Attempt: 2, WorktreePath: "/worktrees/task", Brief: "complete generated brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionName != "fm-lab-contract" || session.WorkspaceID != "w1" ||
		session.TabID != "w1:t1" || session.PaneID != "w1:p1" ||
		session.AgentName != "pi-tsk_contract-a2" || session.AgentSessionID != "codex-session-2" {
		t.Fatalf("session = %+v", session)
	}
	want := [][]string{
		{"workspace", "create", "--cwd", "/worktrees/task", "--label", "Parallel Intellect", "--no-focus", "--session", "fm-lab-contract"},
		{"pane", "run", "w1:p1", "codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust", "--session", "fm-lab-contract"},
		{"pane", "get", "w1:p1", "--session", "fm-lab-contract"},
		{"agent", "get", "w1:p1", "--session", "fm-lab-contract"},
		{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
		{"agent", "prompt", "w1:p1", "complete generated brief", "--wait", "--until", "working", "--timeout", "30000", "--session", "fm-lab-contract"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, want)
	}
}

func TestCommandAdapterHandlesFreshCodexLaunchScreens(t *testing.T) {
	trustScreen := `Do you trust the contents of this directory?

1. Yes, continue`
	hooksScreen := `Do you trust these hooks?

Continue without trusting`
	tests := []struct {
		name      string
		responses []runnerResponse
		wantCalls [][]string
		wantErr   string
	}{
		{
			name:      "folder trust then composer",
			responses: []runnerResponse{{stdout: trustScreen}, {stdout: `{"result":{"type":"keys_sent"}}`}, {stdout: "OpenAI Codex"}},
			wantCalls: [][]string{
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "send-keys", "w1:p1", "enter", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
			},
		},
		{
			name:      "folder trust and hooks then composer",
			responses: []runnerResponse{{stdout: trustScreen}, {stdout: `{"result":{"type":"keys_sent"}}`}, {stdout: hooksScreen}, {stdout: `{"result":{"type":"keys_sent"}}`}, {stdout: "OpenAI Codex"}},
			wantCalls: [][]string{
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "send-keys", "w1:p1", "enter", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "send-keys", "w1:p1", "down", "down", "enter", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
			},
		},
		{
			name:      "composer directly",
			responses: []runnerResponse{{stdout: "OpenAI Codex"}},
			wantCalls: [][]string{{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"}},
		},
		{
			name:      "unknown screen is informative",
			responses: []runnerResponse{{stdout: "A different confirmation screen"}},
			wantErr:   "visible pane:\nA different confirmation screen",
			wantCalls: [][]string{{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: test.responses}
			adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
			err := adapter.waitForCodexComposer(context.Background(), "w1:p1")
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("wait error = %v, want content %q", err, test.wantErr)
			}
			if !reflect.DeepEqual(runner.calls, test.wantCalls) {
				t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, test.wantCalls)
			}
		})
	}
}

func TestCommandAdapterObservesRunningIdleAndLost(t *testing.T) {
	present := `{"result":{"pane":{"pane_id":"w1:p1"}}}`
	tests := []struct {
		name      string
		responses []runnerResponse
		want      State
	}{
		{name: "running", responses: []runnerResponse{{stdout: present}, {stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"working","state_change_seq":1}}}`}}, want: StateRunning},
		{name: "idle", responses: []runnerResponse{{stdout: present}, {stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle","state_change_seq":1}}}`}}, want: StateIdle},
		{name: "done is idle", responses: []runnerResponse{{stdout: present}, {stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"done","state_change_seq":1}}}`}}, want: StateIdle},
		{name: "blocked is idle", responses: []runnerResponse{{stdout: present}, {stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"blocked","state_change_seq":1}}}`}}, want: StateIdle},
		{name: "restored metadata is husk", responses: []runnerResponse{{stdout: present}, {stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle","state_change_seq":0}}}`}}, want: StateHusk},
		{name: "agent-less pane is husk", responses: []runnerResponse{{stdout: present}, {stdout: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")}}, want: StateHusk},
		{name: "pane disappears during probe", responses: []runnerResponse{{stdout: present}, {stdout: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}}, want: StateLost},
		{name: "lost pane", responses: []runnerResponse{{stdout: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}}, want: StateLost},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: test.responses}
			adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
			state, err := adapter.Observe(context.Background(), Session{PaneID: "w1:p1"})
			if err != nil {
				t.Fatal(err)
			}
			if state != test.want {
				t.Fatalf("state = %s, want %s", state, test.want)
			}
			for _, call := range runner.calls {
				if got := call[len(call)-2:]; !reflect.DeepEqual(got, []string{"--session", "fm-lab-contract"}) {
					t.Fatalf("Herdr session routing = %#v", call)
				}
			}
		})
	}
}

func TestCommandAdapterWakesSamePane(t *testing.T) {
	runner := &fakeRunner{responses: []runnerResponse{
		{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle","state_change_seq":1}}}`},
		{stdout: `{"result":{"type":"prompt_sent"}}`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
	session := Session{SessionName: "fm-lab-contract", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1"}
	woken, err := adapter.Wake(context.Background(), session, "address the review feedback")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(woken, session) {
		t.Fatalf("live wake changed placement: before=%+v after=%+v", session, woken)
	}
	want := [][]string{
		{"pane", "get", "w1:p1", "--session", "fm-lab-contract"},
		{"agent", "get", "w1:p1", "--session", "fm-lab-contract"},
		{"agent", "prompt", "w1:p1", "address the review feedback", "--wait", "--until", "working", "--timeout", "30000", "--session", "fm-lab-contract"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, want)
	}
}

func TestCommandAdapterCreatesReplacementBeforeClosingHusk(t *testing.T) {
	runner := &fakeRunner{responses: []runnerResponse{
		{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")},
		{stdout: `{"result":{"tabs":[{"tab_id":"w1:t1","label":"pi-worker"}]}}`},
		{stdout: `{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}`},
		{stdout: `{"result":{"type":"command_started"}}`},
		{stdout: `{"result":{"pane":{"pane_id":"w1:p2"}}}`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p2","agent_status":"idle","state_change_seq":1}}}`},
		{stdout: `OpenAI Codex`},
		{stdout: `{"result":{"type":"prompt_sent"}}`},
		{stdout: `{"result":{"type":"tab_closed"}}`},
		{stdout: `{"result":{"tabs":[{"tab_id":"w1:t2","label":"pi-worker"}]}}`},
		{stdout: `{"result":{"tabs":[{"tab_id":"w1:t2","label":"pi-worker"}]}}`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
	session := Session{SessionName: "fm-lab-contract", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
		AgentName: "pi-task-a1", AgentSessionID: "codex-session-1", WorktreePath: "/worktrees/task"}
	woken, err := adapter.Wake(context.Background(), session, "continue after restart")
	if err != nil {
		t.Fatal(err)
	}
	if woken.TabID != "w1:t2" || woken.PaneID != "w1:p2" || woken.WorkspaceID != session.WorkspaceID ||
		woken.AgentSessionID != session.AgentSessionID {
		t.Fatalf("replacement session = %+v", woken)
	}
	want := [][]string{
		{"pane", "get", "w1:p1", "--session", "fm-lab-contract"},
		{"agent", "get", "w1:p1", "--session", "fm-lab-contract"},
		{"tab", "list", "--workspace", "w1", "--session", "fm-lab-contract"},
		{"tab", "create", "--workspace", "w1", "--cwd", "/worktrees/task", "--label", "pi-worker", "--no-focus", "--session", "fm-lab-contract"},
		{"pane", "run", "w1:p2", "codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust resume codex-session-1", "--session", "fm-lab-contract"},
		{"pane", "get", "w1:p2", "--session", "fm-lab-contract"},
		{"agent", "get", "w1:p2", "--session", "fm-lab-contract"},
		{"pane", "read", "w1:p2", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
		{"agent", "prompt", "w1:p2", "continue after restart", "--wait", "--until", "working", "--timeout", "30000", "--session", "fm-lab-contract"},
		{"tab", "close", "w1:t1", "--session", "fm-lab-contract"},
		{"tab", "list", "--workspace", "w1", "--session", "fm-lab-contract"},
		{"tab", "list", "--workspace", "w1", "--session", "fm-lab-contract"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, want)
	}
}

func TestCommandAdapterLeavesHuskWhenReplacementIsNotVerified(t *testing.T) {
	runner := &fakeRunner{responses: []runnerResponse{
		{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")},
		{stdout: `{"result":{"tabs":[{"tab_id":"w1:t1","label":"pi-worker"}]}}`},
		{stdout: `{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}`},
		{stdout: `{"result":{"type":"command_started"}}`},
		{stdout: `{"result":{"pane":{"pane_id":"w1:p2"}}}`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p2","agent_status":"idle","state_change_seq":1}}}`},
		{stdout: `OpenAI Codex`},
		{stderr: `{"error":{"code":"agent_prompt_stalled"}}`, err: errors.New("exit 1")},
		{stdout: `resumed but prompt was not accepted`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
	_, err := adapter.Wake(context.Background(), Session{SessionName: "fm-lab-contract", WorkspaceID: "w1",
		TabID: "w1:t1", PaneID: "w1:p1", AgentName: "pi-task-a1", AgentSessionID: "codex-session-1",
		WorktreePath: "/worktrees/task"}, "continue after restart")
	if err == nil || !strings.Contains(err.Error(), "visible replacement pane") {
		t.Fatalf("unverified replacement error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call) >= 2 && call[0] == "tab" && call[1] == "close" {
			t.Fatalf("closed husk before replacement prompt was verified: %#v", runner.calls)
		}
	}
}

func TestCommandAdapterMissingPaneIsNotRelaunched(t *testing.T) {
	runner := &fakeRunner{responses: []runnerResponse{{stdout: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
	_, err := adapter.Wake(context.Background(), Session{SessionName: "fm-lab-contract", PaneID: "w1:p1",
		AgentName: "pi-task-a1", AgentSessionID: "codex-session-1"}, "continue")
	if !errors.Is(err, ErrSessionMissing) || len(runner.calls) != 1 {
		t.Fatalf("missing-pane wake error = %v, calls=%#v", err, runner.calls)
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

func TestRealHerdrPersistentWorkerSmoke(t *testing.T) {
	if os.Getenv("PARALLEL_INTELLECT_HERDR_SMOKE") != "1" {
		t.Skip("set PARALLEL_INTELLECT_HERDR_SMOKE=1 to exercise Herdr in an isolated lab session")
	}
	helper := os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		helper = "/Users/kevin/github/kvkenyon/research/firstmate/bin/fm-herdr-lab.sh"
	}
	sessionName := strings.TrimSpace(os.Getenv("HERDR_LAB_SESSION"))
	if sessionName == "" {
		nameOutput, err := exec.Command(helper, "name", "parallel-intellect-m4-persistent-workers").Output()
		if err != nil {
			t.Fatal(err)
		}
		sessionName = strings.TrimSpace(string(nameOutput))
	}
	if !strings.HasPrefix(sessionName, "fm-lab-") || sessionName == "default" {
		t.Fatalf("unsafe Herdr lab session %q", sessionName)
	}
	if os.Getenv("HERDR_LAB_PROVISIONED") != "1" {
		provisionAttempted := false
		t.Cleanup(func() {
			if !provisionAttempted {
				return
			}
			if output, err := exec.Command(helper, "teardown", sessionName).CombinedOutput(); err != nil {
				t.Errorf("teardown Herdr lab: %v: %s", err, output)
			}
		})
		provisionAttempted = true
		if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
			t.Fatalf("provision Herdr lab: %v: %s", err, output)
		}
	}

	worktree, err := os.MkdirTemp(".", ".herdr-m4-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	worktree, err = filepath.Abs(worktree)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(worktree) })
	runner := labRunner{helper: helper, session: sessionName}
	adapter := NewCommandAdapterWithRunner(sessionName, "pi-m4-persistent-worker", runner)
	session, err := adapter.StartCodex(context.Background(), StartRequest{
		TaskID: "tsk_m4_smoke", Attempt: 1, WorktreePath: worktree,
		Brief: "This is an isolated lifecycle smoke. Run `sleep 1`, then reply briefly and wait for another message.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionName != sessionName || session.PaneID == "" || session.AgentSessionID == "" || session.AgentName == "" {
		t.Fatalf("unsafe or incomplete runtime identity: %+v", session)
	}
	waitForHerdrState(t, adapter, session, StateIdle, 3*time.Minute)
	assertPaneCWD(t, runner, session, worktree)
	original := session
	liveMarker := filepath.Join(worktree, "live-wake-ok")
	woken, err := adapter.Wake(context.Background(), session,
		"Live wake smoke: run `sleep 3; printf live-wake-ok > live-wake-ok`, reply briefly, then become idle again.")
	if err != nil {
		t.Fatal(err)
	}
	if woken != original {
		t.Fatalf("live wake changed session identity: before=%+v after=%+v", original, woken)
	}
	if state, observeErr := adapter.Observe(context.Background(), session); observeErr != nil || state != StateRunning {
		t.Fatalf("live prompt did not produce idle -> working: state=%s error=%v\nvisible pane:\n%s",
			state, observeErr, capturePane(t, runner, session.PaneID))
	}
	t.Logf("live prompt submitted; visible pane while working:\n%s", capturePane(t, runner, session.PaneID))
	waitForHerdrState(t, adapter, session, StateIdle, 3*time.Minute)
	if _, err := os.Stat(liveMarker); err != nil {
		t.Logf("live wake completed working -> idle but secondary marker %s is absent: %v\nvisible pane:\n%s",
			liveMarker, err, capturePane(t, runner, session.PaneID))
	}
	if os.Getenv("PARALLEL_INTELLECT_HERDR_SMOKE_LIVE_ONLY") == "1" {
		return
	}

	if output, err := exec.Command(helper, "stop", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("guarded stop of Herdr lab: %v: %s", err, output)
	}
	// Let the named server fully retire before reprovisioning the persisted
	// layout; otherwise a fast attach can observe the old registration.
	time.Sleep(500 * time.Millisecond)
	if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("guarded reprovision of Herdr lab: %v: %s", err, output)
	}
	state, err := adapter.Observe(context.Background(), session)
	if err != nil || state != StateHusk {
		t.Fatalf("restored pane liveness = %s, %v; want husk", state, err)
	}
	restartMarker := filepath.Join(worktree, "restart-wake-ok")
	replacement, err := adapter.Wake(context.Background(), session,
		"Restart wake smoke: run `sleep 3; printf restart-wake-ok > restart-wake-ok`, reply briefly, then become idle again.")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.WorkspaceID != original.WorkspaceID || replacement.TabID == original.TabID || replacement.PaneID == original.PaneID ||
		replacement.AgentSessionID != original.AgentSessionID {
		t.Fatalf("husk replacement identity: before=%+v after=%+v", original, replacement)
	}
	assertPaneCWD(t, runner, replacement, worktree)
	if state, observeErr := adapter.Observe(context.Background(), replacement); observeErr != nil || state != StateRunning {
		t.Fatalf("resumed prompt did not produce idle -> working: state=%s error=%v\nvisible pane:\n%s",
			state, observeErr, capturePane(t, runner, replacement.PaneID))
	}
	t.Logf("resumed prompt submitted in replacement pane while working:\n%s", capturePane(t, runner, replacement.PaneID))
	oldState, oldErr := adapter.Observe(context.Background(), original)
	if oldErr != nil || oldState != StateLost {
		t.Fatalf("old husk after verified replacement = %s, %v; want lost", oldState, oldErr)
	}
	waitForHerdrState(t, adapter, replacement, StateIdle, 3*time.Minute)
	if _, err := os.Stat(restartMarker); err != nil {
		t.Logf("restart wake completed working -> idle but secondary marker %s is absent: %v\nvisible pane:\n%s",
			restartMarker, err, capturePane(t, runner, replacement.PaneID))
	}
}

func assertPaneCWD(t *testing.T, runner labRunner, session Session, want string) {
	t.Helper()
	stdout, stderr, err := runner.Run(context.Background(), "pane", "get", session.PaneID,
		"--session", runner.session)
	if err != nil {
		t.Fatalf("read pane cwd: %v: %s", err, stderr)
	}
	var response struct {
		Result struct {
			Pane struct {
				ForegroundCWD string `json:"foreground_cwd"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		t.Fatalf("decode pane cwd: %v: %s", err, stdout)
	}
	got, err := filepath.EvalSymlinks(response.Result.Pane.ForegroundCWD)
	if err != nil {
		t.Fatalf("resolve pane cwd %q: %v", response.Result.Pane.ForegroundCWD, err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolve expected pane cwd %q: %v", want, err)
	}
	if got != want {
		t.Fatalf("pane foreground cwd = %q, marker contract expects %q", got, want)
	}
}

func capturePane(t *testing.T, runner labRunner, paneID string) string {
	t.Helper()
	stdout, stderr, err := runner.Run(context.Background(), "pane", "read", paneID,
		"--source", "recent", "--lines", "200", "--session", runner.session)
	if err != nil {
		return "<capture failed: " + err.Error() + ": " + string(stderr) + ">"
	}
	return string(stdout)
}

func waitForHerdrState(t *testing.T, adapter *CommandAdapter, session Session, want State, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var last State
	for {
		state, err := adapter.Observe(ctx, session)
		if err == nil {
			last = state
			if state == want {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Herdr state did not reach %s (last=%s, error=%v)", want, last, err)
		case <-ticker.C:
		}
	}
}
