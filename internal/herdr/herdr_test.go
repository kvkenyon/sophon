package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func piFixture(t *testing.T) (worktree, extension string) {
	t.Helper()
	root := t.TempDir()
	worktree = filepath.Join(root, "worktree")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	extension = filepath.Join(state, "pi-lifecycle.ts")
	if err := os.WriteFile(extension, []byte("export default () => {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return worktree, extension
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
		{stdout: `{"result":{"tab":{"tab_id":"w1:t1"}}}`},
		{stdout: `{"result":{"type":"command_started"}}`},
		{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_status":"idle","state_change_seq":1}}}`},
		{stdout: `OpenAI Codex`},
		{stdout: `{"result":{"agent":{"pane_id":"w1:p1","agent_session":{"value":"codex-session-2"}},"type":"prompt_sent"}}`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
	session, err := adapter.StartCodex(context.Background(), StartRequest{
		TaskID: "tsk_contract", TaskTitle: "Fix contract launch", Attempt: 2, WorktreePath: "/worktrees/task", Brief: "complete generated brief",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionName != "fm-lab-contract" || session.WorkspaceID != "w1" ||
		session.TabID != "w1:t1" || session.PaneID != "w1:p1" ||
		session.AgentName != "pi-fix-contract-launch-tskcontr-a2" || session.AgentSessionID != "codex-session-2" {
		t.Fatalf("session = %+v", session)
	}
	want := [][]string{
		{"workspace", "create", "--cwd", "/worktrees/task", "--label", "pintellect", "--no-focus", "--session", "fm-lab-contract"},
		{"tab", "rename", "w1:t1", "pi-fix-contract-launch-tskcontr-a2", "--session", "fm-lab-contract"},
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

func TestHerdrRuntimeConformanceStartsClaudeAndPiWithLaunchProfiles(t *testing.T) {
	piWorktree, piExtension := piFixture(t)
	tests := []struct {
		name          string
		runtime       Runtime
		request       StartRequest
		composer      string
		sessionID     string
		launchCommand string
	}{
		{
			name: "claude", runtime: RuntimeClaude,
			request: StartRequest{TaskID: "tsk_claude", Attempt: 1, WorktreePath: "/worktrees/claude",
				Brief: "complete Claude brief", Model: "claude-opus-5"},
			composer: "Claude Code\n❯", sessionID: "claude-session-1",
			launchCommand: "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false claude --dangerously-skip-permissions --model 'claude-opus-5' 'complete Claude brief'",
		},
		{
			name: "pi", runtime: RuntimePi,
			request: StartRequest{TaskID: "tsk_pi", Attempt: 2, WorktreePath: piWorktree,
				Brief: "complete Pi brief", Model: "kimi-coding/k3-256k", PiExtensionPath: piExtension},
			composer: "pi v0.84.0\nescape interrupt", sessionID: "/sessions/pi-session.jsonl",
			launchCommand: "FM_PI_HARNESS=pi pi --model 'kimi-coding/k3-256k' -e " + shellQuote(piExtension) + " 'complete Pi brief'",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []runnerResponse{
				{stdout: `{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}`},
				{stdout: `{"result":{"tab":{"tab_id":"w1:t1"}}}`},
				{stdout: `{"result":{"type":"command_started"}}`},
				{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
				{stdout: `{"result":{"agent":{"agent":"` + string(test.runtime) + `","pane_id":"w1:p1","agent_status":"working","state_change_seq":1}}}`},
				{stdout: test.composer},
				{stdout: `{"result":{"agent":{"agent":"` + string(test.runtime) + `","pane_id":"w1:p1","agent_session":{"value":"` + test.sessionID + `"}}}}`},
			}}
			adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
			request := test.request
			request.Runtime = test.runtime
			session, err := adapter.Start(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if session.Runtime != test.runtime || session.AgentSessionID != test.sessionID || session.PaneID != "w1:p1" {
				t.Fatalf("session = %+v", session)
			}
			want := [][]string{
				{"workspace", "create", "--cwd", request.WorktreePath, "--label", "pintellect", "--no-focus", "--session", "fm-lab-contract"},
				{"tab", "rename", "w1:t1", session.AgentName, "--session", "fm-lab-contract"},
				{"pane", "run", "w1:p1", test.launchCommand, "--session", "fm-lab-contract"},
				{"pane", "get", "w1:p1", "--session", "fm-lab-contract"},
				{"agent", "get", "w1:p1", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"agent", "get", "w1:p1", "--session", "fm-lab-contract"},
			}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, want)
			}
		})
	}
}

func TestPiLaunchProfileRequiresExternalLifecycleExtension(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	extension := filepath.Join(worktree, "turn-end.ts")
	if err := os.WriteFile(extension, []byte("export default () => {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", &fakeRunner{})
	_, err := adapter.StartPi(context.Background(), StartRequest{TaskID: "tsk_pi", Attempt: 1,
		WorktreePath: worktree, Brief: "brief", Model: "provider/model", PiExtensionPath: extension})
	if err == nil || !strings.Contains(err.Error(), "outside the task worktree") {
		t.Fatalf("inside-worktree extension error = %v", err)
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

func TestHerdrRuntimeConformanceHandlesClaudeAndPiFirstLaunchScreensStructurally(t *testing.T) {
	tests := []struct {
		name      string
		runtime   Runtime
		responses []runnerResponse
		wantCalls [][]string
		wantErr   string
	}{
		{
			name: "claude folder trust and bypass warning", runtime: RuntimeClaude,
			responses: []runnerResponse{
				{stdout: "Do you trust the files in this folder?\nYes, proceed"},
				{stdout: `{"result":{"type":"keys_sent"}}`},
				{stdout: "Bypass Permissions mode\nYes, I accept"},
				{stdout: `{"result":{"type":"keys_sent"}}`},
				{stdout: "Claude Code\n❯"},
			},
			wantCalls: [][]string{
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "send-keys", "w1:p1", "enter", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "send-keys", "w1:p1", "enter", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
			},
		},
		{
			name: "pi project trust", runtime: RuntimePi,
			responses: []runnerResponse{
				{stdout: "Trust project folder?\n/worktree\nThis allows pi to load .pi settings and resources.\nTrust"},
				{stdout: `{"result":{"type":"keys_sent"}}`},
				{stdout: "pi v0.84.0\nescape interrupt"},
			},
			wantCalls: [][]string{
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "send-keys", "w1:p1", "enter", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
			},
		},
		{
			name: "claude shell echo is a keyless transient", runtime: RuntimeClaude,
			responses: []runnerResponse{
				{stdout: "❯ CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false claude --\ndangerously-skip-permissions"},
				{stdout: "Claude Code\n❯"},
			},
			wantCalls: [][]string{
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
			},
		},
		{
			name: "pi shell echo is a keyless transient", runtime: RuntimePi,
			responses: []runnerResponse{
				{stdout: "❯ FM_PI_HARNESS=pi pi --model 'provider/model'"},
				{stdout: "pi v0.84.0\nescape interrupt"},
			},
			wantCalls: [][]string{
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
				{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"},
			},
		},
		{
			name: "unknown claude screen is not keyed", runtime: RuntimeClaude,
			responses: []runnerResponse{{stdout: "A different confirmation screen"}},
			wantCalls: [][]string{{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"}},
			wantErr:   "visible pane:\nA different confirmation screen",
		},
		{
			name: "unknown pi screen is not keyed", runtime: RuntimePi,
			responses: []runnerResponse{{stdout: "A different confirmation screen"}},
			wantCalls: [][]string{{"pane", "read", "w1:p1", "--source", "recent", "--lines", "200", "--session", "fm-lab-contract"}},
			wantErr:   "visible pane:\nA different confirmation screen",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: test.responses}
			adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
			err := adapter.waitForComposer(context.Background(), Session{Runtime: test.runtime, PaneID: "w1:p1"})
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

func TestHerdrRuntimeConformancePromptIdleCancelAndLost(t *testing.T) {
	for _, runtime := range []Runtime{RuntimeCodex, RuntimeClaude, RuntimePi} {
		t.Run(string(runtime), func(t *testing.T) {
			session := Session{Runtime: runtime, SessionName: "fm-lab-contract", PaneID: "w1:p1"}
			present := `{"result":{"pane":{"pane_id":"w1:p1"}}}`
			idle := `{"result":{"agent":{"agent":"` + string(runtime) + `","pane_id":"w1:p1","agent_status":"idle","state_change_seq":2}}}`
			working := `{"result":{"agent":{"agent":"` + string(runtime) + `","pane_id":"w1:p1","agent_status":"working","state_change_seq":1}}}`

			t.Run("prompt", func(t *testing.T) {
				runner := &fakeRunner{responses: []runnerResponse{
					{stdout: present}, {stdout: idle}, {stdout: `{"result":{"type":"prompt_sent"}}`},
				}}
				adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
				if _, err := adapter.Wake(context.Background(), session, "next prompt"); err != nil {
					t.Fatal(err)
				}
				if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got,
					[]string{"agent", "prompt", "w1:p1", "next prompt", "--wait", "--until", "working", "--timeout", "30000", "--session", "fm-lab-contract"}) {
					t.Fatalf("prompt call = %#v", got)
				}
			})

			t.Run("idle", func(t *testing.T) {
				runner := &fakeRunner{responses: []runnerResponse{{stdout: present}, {stdout: idle}}}
				adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
				state, err := adapter.Observe(context.Background(), session)
				if err != nil || state != StateIdle {
					t.Fatalf("idle observation = %s, %v", state, err)
				}
			})

			t.Run("cancel", func(t *testing.T) {
				runner := &fakeRunner{responses: []runnerResponse{
					{stdout: present}, {stdout: working}, {stdout: `{"result":{"type":"keys_sent"}}`},
					{stdout: present}, {stdout: idle},
				}}
				adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
				if err := adapter.Cancel(context.Background(), session); err != nil {
					t.Fatal(err)
				}
				if got := runner.calls[2]; !reflect.DeepEqual(got,
					[]string{"pane", "send-keys", "w1:p1", "escape", "--session", "fm-lab-contract"}) {
					t.Fatalf("cancel call = %#v", got)
				}
			})

			t.Run("lost process", func(t *testing.T) {
				runner := &fakeRunner{responses: []runnerResponse{{stdout: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}}}
				adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
				state, err := adapter.Observe(context.Background(), session)
				if err != nil || state != StateLost {
					t.Fatalf("lost observation = %s, %v", state, err)
				}
			})
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
		{"agent", "prompt", "w1:p2", "continue after restart", "--wait", "--until", "working", "--timeout", "30000", "--session", "fm-lab-contract"},
		{"tab", "close", "w1:t1", "--session", "fm-lab-contract"},
		{"tab", "list", "--workspace", "w1", "--session", "fm-lab-contract"},
		{"tab", "list", "--workspace", "w1", "--session", "fm-lab-contract"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Herdr calls = %#v, want %#v", runner.calls, want)
	}
}

func TestHerdrRuntimeConformanceResumesClaudeAndPiAfterRestart(t *testing.T) {
	piWorktree, piExtension := piFixture(t)
	tests := []struct {
		name          string
		session       Session
		resumeCommand string
	}{
		{
			name: "claude",
			session: Session{Runtime: RuntimeClaude, SessionName: "fm-lab-contract", WorkspaceID: "w1",
				TabID: "w1:t1", PaneID: "w1:p1", AgentName: "pi-task-a1", AgentSessionID: "claude-session-1",
				WorktreePath: "/worktrees/claude", Model: "claude-opus-5"},
			resumeCommand: "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false claude --dangerously-skip-permissions --model 'claude-opus-5' --resume claude-session-1",
		},
		{
			name: "pi",
			session: Session{Runtime: RuntimePi, SessionName: "fm-lab-contract", WorkspaceID: "w1",
				TabID: "w1:t1", PaneID: "w1:p1", AgentName: "pi-task-a1", AgentSessionID: "/sessions/pi-session.jsonl",
				WorktreePath: piWorktree, Model: "kimi-coding/k3-256k", PiExtensionPath: piExtension},
			resumeCommand: "FM_PI_HARNESS=pi pi --model 'kimi-coding/k3-256k' -e " + shellQuote(piExtension) +
				" --session '/sessions/pi-session.jsonl'",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := test.session.Runtime
			runner := &fakeRunner{responses: []runnerResponse{
				{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
				{stdout: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")},
				{stdout: `{"result":{"tabs":[{"tab_id":"w1:t1","label":"worker"}]}}`},
				{stdout: `{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}`},
				{stdout: `{"result":{"type":"command_started"}}`},
				{stdout: `{"result":{"pane":{"pane_id":"w1:p2"}}}`},
				{stdout: `{"result":{"agent":{"agent":"` + string(runtime) + `","pane_id":"w1:p2","agent_status":"idle","state_change_seq":1}}}`},
				{stdout: `{"result":{"type":"prompt_sent"}}`},
				{stdout: `{"result":{"type":"tab_closed"}}`},
				{stdout: `{"result":{"tabs":[{"tab_id":"w1:t2","label":"worker"}]}}`},
				{stdout: `{"result":{"tabs":[{"tab_id":"w1:t2","label":"worker"}]}}`},
			}}
			adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
			replacement, err := adapter.Wake(context.Background(), test.session, "continue after restart")
			if err != nil {
				t.Fatal(err)
			}
			if replacement.Runtime != runtime || replacement.TabID != "w1:t2" || replacement.PaneID != "w1:p2" ||
				replacement.AgentSessionID != test.session.AgentSessionID {
				t.Fatalf("replacement = %+v", replacement)
			}
			if got := runner.calls[4]; !reflect.DeepEqual(got,
				[]string{"pane", "run", "w1:p2", test.resumeCommand, "--session", "fm-lab-contract"}) {
				t.Fatalf("resume call = %#v", got)
			}
			if got := runner.calls[8]; !reflect.DeepEqual(got,
				[]string{"tab", "close", "w1:t1", "--session", "fm-lab-contract"}) {
				t.Fatalf("husk close call = %#v", got)
			}
		})
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

func TestCommandAdapterSubmitSteersRunningAgent(t *testing.T) {
	runner := &fakeRunner{responses: []runnerResponse{
		{stdout: `{"result":{"pane":{"pane_id":"w1:p1"}}}`},
		{stdout: `{"result":{"agent":{"agent":"codex","pane_id":"w1:p1","agent_status":"working","state_change_seq":2}}}`},
		{stdout: `{"result":{"ok":true}}`},
	}}
	adapter := NewCommandAdapterWithRunner("fm-lab-contract", "", runner)
	session := Session{Runtime: RuntimeCodex, SessionName: "fm-lab-contract", PaneID: "w1:p1"}
	updated, err := adapter.Submit(context.Background(), session, "bounded commander steer")
	if err != nil {
		t.Fatal(err)
	}
	if updated != session {
		t.Fatalf("running submit changed session: before=%+v after=%+v", session, updated)
	}
	want := []string{"agent", "prompt", "w1:p1", "bounded commander steer", "--wait", "--until", "working", "--timeout", "30000", "--session", "fm-lab-contract"}
	if len(runner.calls) != 3 || !reflect.DeepEqual(runner.calls[2], want) {
		t.Fatalf("submit calls = %#v", runner.calls)
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

func TestRealHerdrClaudePiConformance(t *testing.T) {
	if os.Getenv("PARALLEL_INTELLECT_HERDR_SMOKE") != "1" {
		t.Skip("set PARALLEL_INTELLECT_HERDR_SMOKE=1 to exercise Claude and Pi in an isolated lab session")
	}
	helper := os.Getenv("HERDR_LAB_HELPER")
	if helper == "" {
		helper = "/Users/kevin/github/kvkenyon/research/firstmate/bin/fm-herdr-lab.sh"
	}
	sessionName := strings.TrimSpace(os.Getenv("HERDR_LAB_SESSION"))
	if sessionName == "" {
		nameOutput, err := exec.Command(helper, "name", "parallel-intellect-m5-claude-pi-workers").Output()
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

	root, err := os.MkdirTemp(".", ".herdr-m5-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	claudeWorktree := filepath.Join(root, "claude-worktree")
	piWorktree := filepath.Join(root, "pi-worktree")
	stateDir := filepath.Join(root, "state")
	for _, path := range []string{claudeWorktree, piWorktree, stateDir} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	piMarker := filepath.Join(stateDir, "pi-turn-end")
	piExtension := filepath.Join(stateDir, "pi-lifecycle.ts")
	extension := fmt.Sprintf(`import { execFile } from "node:child_process";
export default function (pi: any) {
  pi.on("turn_end", () => execFile("touch", [%q]));
}
`, piMarker)
	if err := os.WriteFile(piExtension, []byte(extension), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := labRunner{helper: helper, session: sessionName}
	adapter := NewCommandAdapterWithRunner(sessionName, "pi-m5-runtime-conformance", runner)
	claude, err := adapter.StartClaude(context.Background(), StartRequest{
		TaskID: "tsk_m5_claude", Attempt: 1, WorktreePath: claudeWorktree,
		Brief: "Reply exactly CLAUDE_M5_START_OK, then wait for another message.",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForHerdrState(t, adapter, claude, StateIdle, 3*time.Minute)
	waitForPaneText(t, runner, claude.PaneID, "CLAUDE_M5_START_OK", time.Minute)

	piModel := strings.TrimSpace(os.Getenv("PARALLEL_INTELLECT_PI_MODEL"))
	if piModel == "" {
		piModel = "kimi-coding/k3-256k"
	}
	piSession, err := adapter.StartPi(context.Background(), StartRequest{
		TaskID: "tsk_m5_pi", Attempt: 1, WorktreePath: piWorktree,
		Brief: "Reply exactly PI_M5_START_OK, then wait for another message.",
		Model: piModel, PiExtensionPath: piExtension,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForHerdrState(t, adapter, piSession, StateIdle, 3*time.Minute)
	waitForPaneText(t, runner, piSession.PaneID, "PI_M5_START_OK", time.Minute)
	waitForFile(t, piMarker, time.Minute)

	for name, session := range map[string]Session{"claude": claude, "pi": piSession} {
		t.Run(name+" prompt and cancel", func(t *testing.T) {
			woken, err := adapter.Wake(context.Background(), session,
				"Run `sleep 20`, then reply with the word finished.")
			if err != nil {
				t.Fatal(err)
			}
			if woken != session {
				t.Fatalf("live prompt changed placement: before=%+v after=%+v", session, woken)
			}
			if err := adapter.Cancel(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			if state, err := adapter.Observe(context.Background(), session); err != nil || state != StateIdle {
				t.Fatalf("post-cancel observation = %s, %v", state, err)
			}
		})
	}

	if output, err := exec.Command(helper, "stop", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("guarded stop of Herdr lab: %v: %s", err, output)
	}
	time.Sleep(500 * time.Millisecond)
	if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("guarded reprovision of Herdr lab: %v: %s", err, output)
	}

	for name, original := range map[string]Session{"claude": claude, "pi": piSession} {
		t.Run(name+" restart resume and lost", func(t *testing.T) {
			if state, err := adapter.Observe(context.Background(), original); err != nil || state != StateHusk {
				t.Fatalf("restored liveness = %s, %v; want husk", state, err)
			}
			replacement, err := adapter.Wake(context.Background(), original,
				"After restart, run `sleep 3`, reply RESUME_M5_OK, then wait.")
			if err != nil {
				t.Fatal(err)
			}
			if replacement.WorkspaceID != original.WorkspaceID || replacement.TabID == original.TabID ||
				replacement.PaneID == original.PaneID || replacement.AgentSessionID != original.AgentSessionID {
				t.Fatalf("replacement identity: before=%+v after=%+v", original, replacement)
			}
			waitForHerdrState(t, adapter, replacement, StateIdle, 3*time.Minute)
			if err := adapter.Stop(context.Background(), replacement); err != nil {
				t.Fatal(err)
			}
			if state, err := adapter.Observe(context.Background(), replacement); err != nil || state != StateLost {
				t.Fatalf("closed process observation = %s, %v; want lost", state, err)
			}
		})
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

func waitForPaneText(t *testing.T, runner labRunner, paneID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		visible := capturePane(t, runner, paneID)
		if strings.Contains(visible, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane did not contain %q before timeout; visible pane:\n%s", want, visible)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s did not appear before timeout", path)
		}
		time.Sleep(250 * time.Millisecond)
	}
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
