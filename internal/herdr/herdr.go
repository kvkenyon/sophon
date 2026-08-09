// Package herdr adapts the Herdr CLI without conflating presentation labels
// with stable pane identity.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	"sophon/internal/naming"
)

type State string

// Runtime identifies a supported interactive worker harness. It is carried
// with Session so restart recovery can select the runtime's native resume
// mechanism instead of inferring it from presentation labels.
type Runtime string

const (
	StateRunning State = "running"
	StateIdle    State = "idle"
	StateHusk    State = "husk"
	StateLost    State = "lost"

	RuntimeCodex  Runtime = "codex"
	RuntimeClaude Runtime = "claude"
	RuntimePi     Runtime = "pi"
)

var (
	ErrSessionMissing = errors.New("Herdr pane is structurally missing")
	ErrSessionHusk    = errors.New("Herdr pane is an agent-less husk without resumable identity")
)

type StartRequest struct {
	TaskID    domain.TaskID
	TaskTitle string
	Attempt   int
	// AgentName lets non-worker callers provide a stable presentation identity
	// while retaining the same runtime launch profiles. Worker callers leave it
	// empty and receive the attempt-derived name.
	AgentName    string
	WorktreePath string
	Brief        string
	Runtime      Runtime
	Model        string

	// DataHome is the exact resolved Sophon data home propagated into the
	// runtime launch environment. It is non-secret configuration; no other
	// environment values cross this boundary.
	DataHome string

	// ParentWorkspace optionally groups the agent as a new tab inside this
	// exact Herdr workspace (the attached commander's workspace) instead of
	// creating an isolated workspace. Presentation only; never inferred by
	// label. When the registered workspace is gone, Start falls back to an
	// isolated workspace and the returned Session records actual placement.
	ParentWorkspace string

	// PiExtensionPath is the absolute path to the Pi lifecycle extension.
	// Pi project extensions are trust-gated, so this file must be outside the
	// task worktree and is passed explicitly with -e.
	PiExtensionPath string
}

type Session struct {
	Runtime         Runtime
	AgentName       string
	AgentSessionID  string
	SessionName     string
	WorkspaceID     string
	TabID           string
	PaneID          string
	WorktreePath    string
	Model           string
	DataHome        string
	PiExtensionPath string
}

// Adapter is the worker-runtime boundary used by the control plane. Tests can
// replace it without launching a terminal or nested agent.
type Adapter interface {
	StartCodex(context.Context, StartRequest) (Session, error)
	Observe(context.Context, Session) (State, error)
	Wake(context.Context, Session, string) (Session, error)
	Submit(context.Context, Session, string) (Session, error)
}

// Stop closes the task-owned tab, which also stops its sole runtime session.
func (a *CommandAdapter) Stop(ctx context.Context, session Session) error {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" || session.TabID == "" {
		return errors.New("Herdr stop requires an explicit session and tab")
	}
	if session.SessionName != "" && session.SessionName != a.SessionName {
		return errors.New("Herdr stop session identity mismatch")
	}
	if _, stderr, err := a.run(ctx, "tab", "close", session.TabID); err != nil {
		return commandError("stop "+string(sessionRuntime(session))+" task tab", err, stderr)
	}
	return nil
}

// Wake prompts a live idle agent in place. A restored agent-less pane is a
// dead husk: Wake creates a replacement tab in the same workspace, resumes
// the persisted runtime session there, verifies the prompt is accepted, and
// only then closes the exact husk tab. The returned placement is authoritative.
func (a *CommandAdapter) Wake(ctx context.Context, session Session, message string) (Session, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" || session.PaneID == "" {
		return Session{}, errors.New("Herdr wake requires an explicit session and pane")
	}
	if session.SessionName != "" && session.SessionName != a.SessionName {
		return Session{}, errors.New("Herdr wake session identity mismatch")
	}
	if strings.TrimSpace(message) == "" {
		return Session{}, errors.New("Herdr wake message is required")
	}
	state, err := a.Observe(ctx, session)
	if err != nil {
		return Session{}, err
	}
	switch state {
	case StateIdle:
		// The registered agent is still alive, so direct prompt submission is
		// the only operation needed and preserves the active harness process.
	case StateHusk:
		if strings.TrimSpace(session.AgentName) == "" || !validAgentSessionID(sessionRuntime(session), session.AgentSessionID) {
			return Session{}, ErrSessionHusk
		}
		replacement, replaceErr := a.replaceHusk(ctx, session, message)
		if replaceErr != nil {
			return Session{}, replaceErr
		}
		return replacement, nil
	case StateLost:
		return Session{}, ErrSessionMissing
	case StateRunning:
		return Session{}, errors.New("Herdr wake refused because the worker is already running")
	default:
		return Session{}, fmt.Errorf("Herdr wake cannot handle liveness state %q", state)
	}
	if err := a.submitPrompt(ctx, session, message, true); err != nil {
		return Session{}, fmt.Errorf("wake %s: %w", sessionRuntime(session), err)
	}
	return session, nil
}

// Submit delivers text to a registered live agent without imposing worker
// idle-state policy. Commander steering and follow-ups intentionally share
// Herdr's terminal-driven prompt transport. Restored husks still go through
// Wake so their persisted native session is resumed create-before-close.
func (a *CommandAdapter) Submit(ctx context.Context, session Session, message string) (Session, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" || session.PaneID == "" {
		return Session{}, errors.New("Herdr submit requires an explicit session and pane")
	}
	if session.SessionName != "" && session.SessionName != a.SessionName {
		return Session{}, errors.New("Herdr submit session identity mismatch")
	}
	if strings.TrimSpace(message) == "" {
		return Session{}, errors.New("Herdr submit message is required")
	}
	state, err := a.Observe(ctx, session)
	if err != nil {
		return Session{}, err
	}
	waitForWorking := false
	switch state {
	case StateHusk:
		return a.Wake(ctx, session, message)
	case StateLost:
		return Session{}, ErrSessionMissing
	case StateIdle:
		waitForWorking = true
	case StateRunning:
	default:
		return Session{}, fmt.Errorf("Herdr submit cannot handle liveness state %q", state)
	}
	if err := a.submitPrompt(ctx, session, message, waitForWorking); err != nil {
		return Session{}, fmt.Errorf("submit to %s: %w", sessionRuntime(session), err)
	}
	return session, nil
}

// submitPrompt invokes Herdr exactly once and validates its positive
// acknowledgement. Idle delivery asks Herdr to wait for the new turn to start;
// running delivery accepts the queue acknowledgement immediately because the
// agent cannot transition from working to working. An error is never retried:
// the prompt may already be queued, so retyping could duplicate it.
func (a *CommandAdapter) submitPrompt(ctx context.Context, session Session, message string, waitForWorking bool) error {
	args := []string{"agent", "prompt", session.PaneID, message}
	if waitForWorking {
		args = append(args, "--wait", "--until", "working", "--timeout", "30000")
	}
	stdout, stderr, runErr := a.run(ctx, args...)
	var response struct {
		Result struct {
			Type  string `json:"type"`
			OK    bool   `json:"ok"`
			Agent struct {
				PaneID string `json:"pane_id"`
			} `json:"agent"`
		} `json:"result"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	body := bytes.TrimSpace(stdout)
	if len(body) == 0 {
		body = bytes.TrimSpace(stderr)
	}
	if err := json.Unmarshal(body, &response); err != nil {
		if runErr != nil {
			return commandError("prompt "+string(sessionRuntime(session)), runErr, stderr)
		}
		return fmt.Errorf("decode Herdr prompt acknowledgement: %w", err)
	}
	if response.Error.Code != "" {
		if runErr != nil {
			return commandError("prompt "+string(sessionRuntime(session)), runErr, stderr)
		}
		return fmt.Errorf("Herdr prompt error %s", response.Error.Code)
	}
	if runErr != nil {
		return commandError("prompt "+string(sessionRuntime(session)), runErr, stderr)
	}
	acceptedType := response.Result.Type == "prompt_sent" || response.Result.Type == "prompt_queued"
	acceptedPane := response.Result.Agent.PaneID == session.PaneID
	if !acceptedType && !response.Result.OK && !acceptedPane {
		return fmt.Errorf("Herdr prompt response did not acknowledge acceptance")
	}
	if response.Result.Agent.PaneID != "" && response.Result.Agent.PaneID != session.PaneID {
		return fmt.Errorf("Herdr prompt acknowledgement did not preserve pane identity")
	}
	return nil
}

// Resume creates a replacement tab for a structurally missing pane. Unlike a
// husk, there is no old tab to label or close; the caller owns retirement of
// the dead durable placement after this replacement has been verified.
func (a *CommandAdapter) Resume(ctx context.Context, missing Session, message string) (Session, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" ||
		missing.WorkspaceID == "" || strings.TrimSpace(missing.WorktreePath) == "" ||
		strings.TrimSpace(missing.AgentName) == "" || !validAgentSessionID(sessionRuntime(missing), missing.AgentSessionID) {
		return Session{}, ErrSessionHusk
	}
	if missing.SessionName != "" && missing.SessionName != a.SessionName {
		return Session{}, errors.New("Herdr resume session identity mismatch")
	}
	if strings.TrimSpace(message) == "" {
		return Session{}, errors.New("Herdr resume message is required")
	}
	stdout, stderr, err := a.run(ctx, "tab", "create", "--workspace", missing.WorkspaceID,
		"--cwd", missing.WorktreePath, "--label", missing.AgentName, "--no-focus")
	if err != nil {
		return Session{}, commandError("create missing commander replacement tab", err, stderr)
	}
	var created struct {
		Result struct {
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout, &created); err != nil {
		return Session{}, fmt.Errorf("decode Herdr missing commander replacement response: %w", err)
	}
	replacement := missing
	replacement.TabID, replacement.PaneID = created.Result.Tab.ID, created.Result.RootPane.ID
	if replacement.TabID == "" || replacement.PaneID == "" || replacement.PaneID == missing.PaneID {
		return Session{}, errors.New("Herdr replacement response omitted distinct tab/pane identity")
	}
	command, err := resumeCommand(replacement)
	if err != nil {
		return Session{}, err
	}
	// Native registration can precede the resumed TUI's input-ready frame.
	// Wait for that bounded composer proof before submitting; otherwise Herdr
	// can accept text while Codex is still restoring and report prompt_stalled.
	if err := a.launchRuntimeInPane(ctx, replacement, command, true); err != nil {
		return Session{}, fmt.Errorf("resume %s in replacement pane: %w", sessionRuntime(missing), err)
	}
	if _, stderr, err := a.run(ctx, "agent", "prompt", replacement.PaneID, message,
		"--wait", "--until", "working", "--timeout", "30000"); err != nil {
		return Session{}, commandError("verify resumed "+string(sessionRuntime(missing)), err, stderr)
	}
	return replacement, nil
}

func (a *CommandAdapter) replaceHusk(ctx context.Context, husk Session, message string) (Session, error) {
	if husk.WorkspaceID == "" || husk.TabID == "" || strings.TrimSpace(husk.WorktreePath) == "" {
		return Session{}, errors.New("Herdr husk replacement requires workspace, tab, and worktree identity")
	}
	label, err := a.exactTabLabel(ctx, husk.WorkspaceID, husk.TabID)
	if err != nil {
		return Session{}, err
	}
	stdout, stderr, err := a.run(ctx, "tab", "create", "--workspace", husk.WorkspaceID,
		"--cwd", husk.WorktreePath, "--label", label, "--no-focus")
	if err != nil {
		return Session{}, commandError("create replacement tab", err, stderr)
	}
	var created struct {
		Result struct {
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout, &created); err != nil {
		return Session{}, fmt.Errorf("decode Herdr replacement tab response: %w", err)
	}
	replacement := husk
	replacement.TabID = created.Result.Tab.ID
	replacement.PaneID = created.Result.RootPane.ID
	if replacement.TabID == "" || replacement.PaneID == "" ||
		replacement.TabID == husk.TabID || replacement.PaneID == husk.PaneID {
		return Session{}, errors.New("Herdr replacement response omitted distinct tab/pane identity")
	}
	command, err := resumeCommand(replacement)
	if err != nil {
		return Session{}, err
	}
	// Native registration can precede the resumed TUI's input-ready frame.
	// Keep the bounded composer proof before prompt submission; the prompt's
	// idle-to-working transition remains the end-to-end readiness proof.
	if err := a.launchRuntimeInPane(ctx, replacement, command, true); err != nil {
		return Session{}, fmt.Errorf("resume %s in replacement pane: %w", sessionRuntime(husk), err)
	}
	if _, stderr, err := a.run(ctx, "agent", "prompt", replacement.PaneID, message,
		"--wait", "--until", "working", "--timeout", "30000"); err != nil {
		promptErr := commandError("verify resumed "+string(sessionRuntime(husk)), err, stderr)
		if visible, captureErr := a.readPane(ctx, replacement.PaneID); captureErr == nil {
			return Session{}, fmt.Errorf("%w; visible replacement pane:\n%s", promptErr, visible)
		}
		return Session{}, promptErr
	}
	// Create and verify the replacement before closing anything. Closing the
	// only tab would destroy the restored workspace.
	if _, stderr, err := a.run(ctx, "tab", "close", husk.TabID); err != nil {
		return Session{}, commandError("close exact Herdr husk tab", err, stderr)
	}
	if _, err := a.exactTabLabel(ctx, husk.WorkspaceID, replacement.TabID); err != nil {
		return Session{}, fmt.Errorf("verify replacement tab: %w", err)
	}
	if _, err := a.exactTabLabel(ctx, husk.WorkspaceID, husk.TabID); !errors.Is(err, ErrSessionMissing) {
		if err == nil {
			return Session{}, errors.New("exact Herdr husk tab still exists after close")
		}
		return Session{}, fmt.Errorf("verify exact Herdr husk removal: %w", err)
	}
	return replacement, nil
}

func (a *CommandAdapter) readPane(ctx context.Context, paneID string) (string, error) {
	stdout, stderr, err := a.run(ctx, "pane", "read", paneID, "--source", "recent", "--lines", "200")
	if err != nil {
		return "", commandError("read pane", err, stderr)
	}
	return string(stdout), nil
}

func (a *CommandAdapter) exactTabLabel(ctx context.Context, workspaceID, tabID string) (string, error) {
	stdout, stderr, err := a.run(ctx, "tab", "list", "--workspace", workspaceID)
	if err != nil {
		return "", commandError("list workspace tabs", err, stderr)
	}
	var response struct {
		Result struct {
			Tabs []struct {
				ID    string `json:"tab_id"`
				Label string `json:"label"`
			} `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		return "", fmt.Errorf("decode Herdr tab list response: %w", err)
	}
	for _, tab := range response.Result.Tabs {
		if tab.ID == tabID {
			if strings.TrimSpace(tab.Label) == "" {
				return "", errors.New("Herdr tab response omitted label")
			}
			return tab.Label, nil
		}
	}
	return "", ErrSessionMissing
}

type CommandRunner interface {
	Run(context.Context, ...string) ([]byte, []byte, error)
}

type execRunner struct{ binary string }

func (r execRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, r.binary, args...)
	stdout, err := command.Output()
	var stderr []byte
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr = exitErr.Stderr
	}
	return stdout, stderr, err
}

type CommandAdapter struct {
	SessionName    string
	WorkspaceLabel string
	runner         CommandRunner
}

func NewCommandAdapter(binary, sessionName, workspaceLabel string) *CommandAdapter {
	if binary == "" {
		binary = "herdr"
	}
	return &CommandAdapter{SessionName: sessionName, WorkspaceLabel: workspaceLabel, runner: execRunner{binary: binary}}
}

func NewCommandAdapterWithRunner(sessionName, workspaceLabel string, runner CommandRunner) *CommandAdapter {
	return &CommandAdapter{SessionName: sessionName, WorkspaceLabel: workspaceLabel, runner: runner}
}

func (a *CommandAdapter) StartCodex(ctx context.Context, in StartRequest) (Session, error) {
	in.Runtime = RuntimeCodex
	return a.Start(ctx, in)
}

func (a *CommandAdapter) StartClaude(ctx context.Context, in StartRequest) (Session, error) {
	in.Runtime = RuntimeClaude
	return a.Start(ctx, in)
}

func (a *CommandAdapter) StartPi(ctx context.Context, in StartRequest) (Session, error) {
	in.Runtime = RuntimePi
	return a.Start(ctx, in)
}

// Start launches any supported runtime. The named helpers above are useful at
// narrow interfaces; this method owns the shared Herdr lifecycle contract.
func (a *CommandAdapter) Start(ctx context.Context, in StartRequest) (Session, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" {
		return Session{}, errors.New("Herdr adapter requires an explicit session and runner")
	}
	if (in.TaskID == "" && strings.TrimSpace(in.AgentName) == "") || in.Attempt < 1 || strings.TrimSpace(in.WorktreePath) == "" || strings.TrimSpace(in.Brief) == "" {
		return Session{}, errors.New("agent identity, attempt, worktree, and brief are required")
	}
	runtime := in.Runtime
	if runtime == "" {
		runtime = RuntimeCodex
	}
	if err := validateRuntimeRequest(runtime, in); err != nil {
		return Session{}, err
	}
	name := strings.TrimSpace(in.AgentName)
	if name == "" {
		name = agentName(in.TaskTitle, in.TaskID, in.Attempt)
	} else {
		name = unsafeName.ReplaceAllString(name, "-")
	}
	if name == "" {
		return Session{}, errors.New("Herdr agent name is empty after normalization")
	}
	workspaceID, tabID, paneID, labeled, err := a.placeAgent(ctx, in, name)
	if err != nil {
		return Session{}, err
	}
	session := Session{
		Runtime: runtime, AgentName: name, SessionName: a.SessionName,
		WorkspaceID: workspaceID, TabID: tabID, PaneID: paneID,
		WorktreePath: in.WorktreePath, Model: strings.TrimSpace(in.Model),
		DataHome: strings.TrimSpace(in.DataHome), PiExtensionPath: strings.TrimSpace(in.PiExtensionPath),
	}
	if !labeled {
		if _, stderr, err := a.run(ctx, "tab", "rename", session.TabID, session.AgentName); err != nil {
			return session, commandError("label task tab", err, stderr)
		}
	}

	command, positionalPrompt, err := initialCommand(runtime, in)
	if err != nil {
		return session, err
	}
	if err := a.launchRuntimeInPane(ctx, session, command, true); err != nil {
		return session, fmt.Errorf("start %s: %w", runtime, err)
	}
	var promptOutput []byte
	if !positionalPrompt {
		var stderr []byte
		promptOutput, stderr, err = a.run(ctx, "agent", "prompt", session.PaneID, in.Brief,
			"--wait", "--until", "working", "--timeout", "30000")
		if err != nil {
			return session, commandError("deliver initial "+string(runtime)+" prompt", err, stderr)
		}
	}
	if session.AgentSessionID, err = a.captureAgentSessionID(ctx, session, promptOutput); err != nil {
		return session, err
	}
	return session, nil
}

// placeAgent chooses the agent's Herdr placement. With an explicit parent
// workspace the agent becomes a labeled tab inside that exact workspace; a
// parent that Herdr reports as missing falls back visibly to an isolated
// workspace, and the returned identifiers always record actual placement.
// The labeled result reports whether the tab label was set at creation.
func (a *CommandAdapter) placeAgent(ctx context.Context, in StartRequest, name string) (workspaceID, tabID, paneID string, labeled bool, err error) {
	if workspace := strings.TrimSpace(in.ParentWorkspace); workspace != "" {
		stdout, stderr, tabErr := a.run(ctx, "tab", "create", "--workspace", workspace,
			"--cwd", in.WorktreePath, "--label", name, "--no-focus")
		if tabErr == nil {
			var created struct {
				Result struct {
					Tab struct {
						ID string `json:"tab_id"`
					} `json:"tab"`
					RootPane struct {
						ID string `json:"pane_id"`
					} `json:"root_pane"`
				} `json:"result"`
			}
			if err := json.Unmarshal(stdout, &created); err != nil {
				return "", "", "", false, fmt.Errorf("decode Herdr tab response: %w", err)
			}
			if created.Result.Tab.ID == "" || created.Result.RootPane.ID == "" {
				return "", "", "", false, errors.New("Herdr tab response omitted stable identifiers")
			}
			return workspace, created.Result.Tab.ID, created.Result.RootPane.ID, true, nil
		}
		if !isWorkspaceMissing(stdout, stderr) {
			return "", "", "", false, commandError("create agent tab in commander workspace", tabErr, stderr)
		}
		// The registered commander workspace is gone. Falling back to an
		// isolated workspace touches no unrelated target; the caller sees the
		// actual placement in the returned session.
	}
	label := strings.TrimSpace(a.WorkspaceLabel)
	if label == "" {
		label = "sophon"
	}
	stdout, stderr, err := a.run(ctx, "workspace", "create", "--cwd", in.WorktreePath, "--label", label, "--no-focus")
	if err != nil {
		return "", "", "", false, commandError("create workspace", err, stderr)
	}
	var created struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout, &created); err != nil {
		return "", "", "", false, fmt.Errorf("decode Herdr workspace response: %w", err)
	}
	if created.Result.Workspace.ID == "" || created.Result.Tab.ID == "" || created.Result.RootPane.ID == "" {
		return "", "", "", false, errors.New("Herdr workspace response omitted stable identifiers")
	}
	return created.Result.Workspace.ID, created.Result.Tab.ID, created.Result.RootPane.ID, false, nil
}

// isWorkspaceMissing reports whether a failed tab-create response says the
// exact parent workspace does not exist. Only that answer permits the
// isolated-workspace fallback; any other failure is an error.
func isWorkspaceMissing(stdout, stderr []byte) bool {
	return bytes.Contains(stdout, []byte("workspace_not_found")) ||
		bytes.Contains(stderr, []byte("workspace_not_found"))
}

func (a *CommandAdapter) launchRuntimeInPane(ctx context.Context, session Session, command string, waitForBanner bool) error {
	_, stderr, err := a.run(ctx, "pane", "run", session.PaneID, command)
	if err != nil {
		return commandError("launch "+string(sessionRuntime(session))+" command", err, stderr)
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, observeErr := a.Observe(ctx, session)
		switch state {
		case StateIdle, StateRunning:
			if !waitForBanner {
				// A resumed TUI restores its transcript and need not redraw the
				// fresh-launch banner. Positive native registration is the resume
				// readiness signal; Wake's prompt --wait/--until working is the
				// subsequent end-to-end submission proof.
				return nil
			}
			// Registration and composer drawing can race in either order. Herdr's
			// wait-output is edge-triggered and misses a banner drawn just before
			// the call, so inspect current pane content until the composer exists.
			return a.waitForComposer(ctx, session)
		case StateLost:
			return ErrSessionMissing
		}
		if observeErr != nil {
			return observeErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%s did not register in the Herdr pane after launch", sessionRuntime(session))
		case <-ticker.C:
		}
	}
}

func (a *CommandAdapter) waitForComposer(ctx context.Context, session Session) error {
	runtime := sessionRuntime(session)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	folderTrustAccepted := false
	hooksDeclined := false
	runtimeTrustAccepted := false
	bypassAccepted := false
	for {
		visible, err := a.readPane(ctx, session.PaneID)
		if err != nil {
			return fmt.Errorf("wait for %s composer: %w", runtime, err)
		}
		switch {
		case runtime == RuntimeCodex && isCodexFolderTrustScreen(visible):
			if !folderTrustAccepted {
				if err := a.sendKeys(ctx, session.PaneID, "enter"); err != nil {
					return fmt.Errorf("accept Codex folder trust: %w", err)
				}
				folderTrustAccepted = true
			}
		case runtime == RuntimeCodex && isCodexHooksTrustScreen(visible):
			if !hooksDeclined {
				// Product workers never trust project hooks. The Codex prompt
				// selects this option with Down, Down, Enter.
				if err := a.sendKeys(ctx, session.PaneID, "down", "down", "enter"); err != nil {
					return fmt.Errorf("decline Codex hooks trust: %w", err)
				}
				hooksDeclined = true
			}
		case runtime == RuntimeClaude && isClaudeFolderTrustScreen(visible):
			if !runtimeTrustAccepted {
				if err := a.sendKeys(ctx, session.PaneID, "enter"); err != nil {
					return fmt.Errorf("accept Claude folder trust: %w", err)
				}
				runtimeTrustAccepted = true
			}
		case runtime == RuntimeClaude && isClaudeBypassScreen(visible):
			if !bypassAccepted {
				if err := a.sendKeys(ctx, session.PaneID, "enter"); err != nil {
					return fmt.Errorf("accept Claude bypass-permissions warning: %w", err)
				}
				bypassAccepted = true
			}
		case runtime == RuntimePi && isPiProjectTrustScreen(visible):
			if !runtimeTrustAccepted {
				if err := a.sendKeys(ctx, session.PaneID, "enter"); err != nil {
					return fmt.Errorf("accept Pi project trust: %w", err)
				}
				runtimeTrustAccepted = true
			}
		case composerReady(runtime, visible):
			return nil
		case isLaunchEcho(runtime, visible):
			// Herdr can capture the shell's echoed command before the TUI's
			// first frame. This is a structurally known transient, not a dialog,
			// so wait without sending any key.
		case strings.TrimSpace(visible) != "":
			return fmt.Errorf("%s composer did not appear; visible pane:\n%s", runtime, visible)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%s composer did not appear; visible pane:\n%s", runtime, visible)
		case <-ticker.C:
		}
	}
}

func isLaunchEcho(runtime Runtime, visible string) bool {
	switch runtime {
	case RuntimeCodex:
		return strings.Contains(visible, "codex --dangerously")
	case RuntimeClaude:
		return strings.Contains(visible, "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false claude --")
	case RuntimePi:
		return strings.Contains(visible, "FM_PI_HARNESS=pi pi --model")
	default:
		return false
	}
}

// waitForCodexComposer preserves the focused regression-test seam for the
// original battle-tested profile.
func (a *CommandAdapter) waitForCodexComposer(ctx context.Context, paneID string) error {
	return a.waitForComposer(ctx, Session{Runtime: RuntimeCodex, PaneID: paneID})
}

func isCodexFolderTrustScreen(visible string) bool {
	return strings.Contains(visible, "Do you trust the contents of this directory?") &&
		strings.Contains(visible, "Yes, continue")
}

func isCodexHooksTrustScreen(visible string) bool {
	return strings.Contains(strings.ToLower(visible), "hooks") &&
		strings.Contains(visible, "Continue without trusting")
}

func isClaudeFolderTrustScreen(visible string) bool {
	lower := strings.ToLower(visible)
	return strings.Contains(lower, "do you trust") &&
		(strings.Contains(lower, "folder") || strings.Contains(lower, "directory")) &&
		strings.Contains(lower, "yes")
}

func isClaudeBypassScreen(visible string) bool {
	lower := strings.ToLower(visible)
	return strings.Contains(lower, "bypass permissions") && strings.Contains(lower, "accept")
}

func isPiProjectTrustScreen(visible string) bool {
	lower := strings.ToLower(visible)
	return strings.Contains(lower, "trust project folder?") &&
		strings.Contains(lower, "this allows pi to load") && strings.Contains(lower, "trust")
}

func composerReady(runtime Runtime, visible string) bool {
	switch runtime {
	case RuntimeCodex:
		return strings.Contains(visible, "OpenAI Codex")
	case RuntimeClaude:
		return strings.Contains(visible, "Claude Code") &&
			(strings.Contains(visible, "bypass permissions on") || strings.Contains(visible, "❯"))
	case RuntimePi:
		return strings.Contains(visible, "pi v") && strings.Contains(visible, "escape interrupt")
	default:
		return false
	}
}

func (a *CommandAdapter) sendKeys(ctx context.Context, paneID string, keys ...string) error {
	args := append([]string{"pane", "send-keys", paneID}, keys...)
	_, stderr, err := a.run(ctx, args...)
	if err != nil {
		return commandError("send keys to runtime", err, stderr)
	}
	return nil
}

// Cancel interrupts the active turn with the runtime-family contract shared
// by Codex, Claude, and Pi: one Escape. It waits for Herdr's semantic state to
// confirm that the turn actually settled.
func (a *CommandAdapter) Cancel(ctx context.Context, session Session) error {
	state, err := a.Observe(ctx, session)
	if err != nil {
		return err
	}
	switch state {
	case StateIdle:
		return nil
	case StateHusk:
		return ErrSessionHusk
	case StateLost:
		return ErrSessionMissing
	case StateRunning:
	default:
		return fmt.Errorf("cannot cancel runtime in state %q", state)
	}
	if err := a.sendKeys(ctx, session.PaneID, "escape"); err != nil {
		return fmt.Errorf("interrupt %s: %w", sessionRuntime(session), err)
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err = a.Observe(ctx, session)
		if err != nil {
			return err
		}
		switch state {
		case StateIdle:
			return nil
		case StateLost:
			return ErrSessionMissing
		case StateHusk:
			return ErrSessionHusk
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%s did not become idle after interrupt", sessionRuntime(session))
		case <-ticker.C:
		}
	}
}

func (a *CommandAdapter) Observe(ctx context.Context, session Session) (State, error) {
	registered, state, err := a.identify(ctx, session)
	if err != nil {
		return "", err
	}
	if registered != "" && registered != sessionRuntime(session) {
		return "", fmt.Errorf("Herdr pane registered %s, want %s", registered, sessionRuntime(session))
	}
	return state, nil
}

// Identify reports the exact pane's registered runtime and liveness state
// without imposing an expected runtime. Commander attach uses it to record
// the ambient identity it was handed; steering paths keep Observe's strict
// runtime check.
func (a *CommandAdapter) Identify(ctx context.Context, session Session) (Runtime, State, error) {
	return a.identify(ctx, session)
}

func (a *CommandAdapter) identify(ctx context.Context, session Session) (Runtime, State, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" || session.PaneID == "" {
		return "", "", errors.New("Herdr observation requires an explicit session and pane")
	}
	if session.SessionName != "" && session.SessionName != a.SessionName {
		return "", "", errors.New("Herdr observation session identity mismatch")
	}
	runtime := sessionRuntime(session)
	stdout, stderr, runErr := a.run(ctx, "pane", "get", session.PaneID)
	body := stdout
	if len(bytes.TrimSpace(body)) == 0 {
		body = stderr
	}
	var paneResponse struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &paneResponse); err != nil {
		if runErr != nil {
			return "", "", commandError("probe "+string(runtime)+" pane", runErr, stderr)
		}
		return "", "", fmt.Errorf("decode Herdr pane response: %w", err)
	}
	if paneResponse.Error.Code == "pane_not_found" {
		return "", StateLost, nil
	}
	if paneResponse.Error.Code != "" {
		return "", "", fmt.Errorf("probe %s pane: Herdr error %s", runtime, paneResponse.Error.Code)
	}
	if runErr != nil {
		return "", "", commandError("probe "+string(runtime)+" pane", runErr, stderr)
	}
	if paneResponse.Result.Pane.PaneID != session.PaneID {
		return "", "", errors.New("Herdr pane response did not preserve pane identity")
	}

	stdout, stderr, runErr = a.run(ctx, "agent", "get", session.PaneID)
	body = stdout
	if len(bytes.TrimSpace(body)) == 0 {
		body = stderr
	}
	var response struct {
		Result struct {
			Agent struct {
				Runtime        string `json:"agent"`
				PaneID         string `json:"pane_id"`
				Status         string `json:"agent_status"`
				StateChangeSeq int64  `json:"state_change_seq"`
			} `json:"agent"`
		} `json:"result"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		if runErr != nil {
			return "", "", commandError("observe "+string(runtime), runErr, stderr)
		}
		return "", "", fmt.Errorf("decode Herdr agent response: %w", err)
	}
	switch response.Error.Code {
	case "agent_not_found":
		return "", StateHusk, nil
	case "pane_not_found":
		return "", StateLost, nil
	case "":
	default:
		return "", "", fmt.Errorf("observe %s: Herdr error %s", runtime, response.Error.Code)
	}
	if runErr != nil {
		return "", "", commandError("observe "+string(runtime), runErr, stderr)
	}
	if response.Result.Agent.PaneID != session.PaneID {
		return "", "", errors.New("Herdr agent response did not preserve pane identity")
	}
	registered := Runtime(strings.TrimSpace(response.Result.Agent.Runtime))
	if response.Result.Agent.StateChangeSeq < 1 {
		// Herdr 0.7.3 may restore the last agent/session presentation with
		// sequence zero even though the harness process and live registration
		// are gone. This is a husk, not a promptable idle agent.
		return "", StateHusk, nil
	}
	switch response.Result.Agent.Status {
	case "working":
		return registered, StateRunning, nil
	case "idle", "done", "blocked":
		return registered, StateIdle, nil
	default:
		return "", "", fmt.Errorf("unknown Herdr agent status %q", response.Result.Agent.Status)
	}
}

func (a *CommandAdapter) captureAgentSessionID(ctx context.Context, session Session, promptResponse []byte) (string, error) {
	runtime := sessionRuntime(session)
	if len(bytes.TrimSpace(promptResponse)) > 0 {
		if value, err := agentSessionID(promptResponse, session.PaneID, runtime); err != nil {
			return "", fmt.Errorf("decode initial %s session identity: %w", runtime, err)
		} else if value != "" {
			return value, nil
		}
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		stdout, stderr, runErr := a.run(ctx, "agent", "get", session.PaneID)
		if runErr != nil {
			return "", commandError("capture "+string(runtime)+" session identity", runErr, stderr)
		}
		value, err := agentSessionID(stdout, session.PaneID, runtime)
		if err != nil {
			return "", fmt.Errorf("decode %s session identity: %w", runtime, err)
		}
		if value != "" {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("Herdr did not report the %s session identity after launch", runtime)
		case <-ticker.C:
		}
	}
}

func agentSessionID(data []byte, paneID string, runtime Runtime) (string, error) {
	var response struct {
		Result struct {
			Agent struct {
				Runtime      string `json:"agent"`
				PaneID       string `json:"pane_id"`
				AgentSession struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", err
	}
	if response.Result.Agent.PaneID != "" && response.Result.Agent.PaneID != paneID {
		return "", errors.New("Herdr agent-session response did not preserve pane identity")
	}
	if response.Result.Agent.Runtime != "" && response.Result.Agent.Runtime != string(runtime) {
		return "", fmt.Errorf("Herdr session belongs to %s, want %s", response.Result.Agent.Runtime, runtime)
	}
	return strings.TrimSpace(response.Result.Agent.AgentSession.Value), nil
}

func (a *CommandAdapter) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	// Herdr 0.7.x requires the explicit session flag on every call. It is
	// deliberately appended last because environment-only routing is unsafe.
	return a.runner.Run(ctx, append(args, "--session", a.SessionName)...)
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var safeAgentSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func sessionRuntime(session Session) Runtime {
	if session.Runtime == "" {
		return RuntimeCodex
	}
	return session.Runtime
}

func validateRuntimeRequest(runtime Runtime, in StartRequest) error {
	switch runtime {
	case RuntimeCodex, RuntimeClaude:
		return nil
	case RuntimePi:
		if strings.TrimSpace(in.Model) == "" {
			return errors.New("Pi launch requires an explicit model")
		}
		return validatePiExtension(in.WorktreePath, in.PiExtensionPath)
	default:
		return fmt.Errorf("unsupported Herdr runtime %q", runtime)
	}
}

func validatePiExtension(worktreePath, extensionPath string) error {
	if !filepath.IsAbs(extensionPath) {
		return errors.New("Pi lifecycle extension path must be absolute")
	}
	info, err := os.Stat(extensionPath)
	if err != nil {
		return fmt.Errorf("read Pi lifecycle extension: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("Pi lifecycle extension must be a regular file")
	}
	worktree, err := filepath.EvalSymlinks(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve Pi worktree: %w", err)
	}
	extension, err := filepath.EvalSymlinks(extensionPath)
	if err != nil {
		return fmt.Errorf("resolve Pi lifecycle extension: %w", err)
	}
	relative, err := filepath.Rel(worktree, extension)
	if err != nil {
		return fmt.Errorf("compare Pi lifecycle extension with worktree: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("Pi lifecycle extension must be outside the task worktree")
	}
	return nil
}

func initialCommand(runtime Runtime, in StartRequest) (command string, positionalPrompt bool, err error) {
	switch runtime {
	case RuntimeCodex:
		return dataHomePrefix(in.DataHome) + "codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust", false, nil
	case RuntimeClaude:
		args := []string{"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false", "claude", "--dangerously-skip-permissions"}
		if model := strings.TrimSpace(in.Model); model != "" {
			args = append(args, "--model", shellQuote(model))
		}
		args = append(args, shellQuote(in.Brief))
		return dataHomePrefix(in.DataHome) + strings.Join(args, " "), true, nil
	case RuntimePi:
		return dataHomePrefix(in.DataHome) + strings.Join([]string{
			"FM_PI_HARNESS=pi", "pi", "--model", shellQuote(strings.TrimSpace(in.Model)),
			"-e", shellQuote(strings.TrimSpace(in.PiExtensionPath)), shellQuote(in.Brief),
		}, " "), true, nil
	default:
		return "", false, fmt.Errorf("unsupported Herdr runtime %q", runtime)
	}
}

// dataHomePrefix renders the exact assigned Sophon data home as a leading
// environment assignment so the runtime process and every command it runs
// select the same store without relying on inherited environment.
func dataHomePrefix(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return datahome.OverrideEnv + "=" + shellQuote(home) + " "
}

func resumeCommand(session Session) (string, error) {
	runtime := sessionRuntime(session)
	if !validAgentSessionID(runtime, session.AgentSessionID) {
		return "", ErrSessionHusk
	}
	switch runtime {
	case RuntimeCodex:
		return dataHomePrefix(session.DataHome) + "codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust resume " + session.AgentSessionID, nil
	case RuntimeClaude:
		args := []string{"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false", "claude", "--dangerously-skip-permissions"}
		if model := strings.TrimSpace(session.Model); model != "" {
			args = append(args, "--model", shellQuote(model))
		}
		args = append(args, "--resume", session.AgentSessionID)
		return dataHomePrefix(session.DataHome) + strings.Join(args, " "), nil
	case RuntimePi:
		if strings.TrimSpace(session.Model) == "" {
			return "", errors.New("resume Pi requires its launch model")
		}
		if err := validatePiExtension(session.WorktreePath, session.PiExtensionPath); err != nil {
			return "", err
		}
		return dataHomePrefix(session.DataHome) + strings.Join([]string{
			"FM_PI_HARNESS=pi", "pi", "--model", shellQuote(strings.TrimSpace(session.Model)),
			"-e", shellQuote(strings.TrimSpace(session.PiExtensionPath)),
			"--session", shellQuote(session.AgentSessionID),
		}, " "), nil
	default:
		return "", fmt.Errorf("unsupported Herdr runtime %q", runtime)
	}
}

func validAgentSessionID(runtime Runtime, value string) bool {
	value = strings.TrimSpace(value)
	switch runtime {
	case RuntimeCodex, RuntimeClaude:
		return safeAgentSessionID.MatchString(value)
	case RuntimePi:
		return filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Ext(value) == ".jsonl" && !strings.ContainsRune(value, '\x00')
	default:
		return false
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func agentName(title string, taskID domain.TaskID, attempt int) string {
	return fmt.Sprintf("pi-%s-a%d", naming.TaskName(title, string(taskID)), attempt)
}

func commandError(operation string, err error, stderr []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		return fmt.Errorf("Herdr %s: %w", operation, err)
	}
	return fmt.Errorf("Herdr %s: %w: %s", operation, err, detail)
}
