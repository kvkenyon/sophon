// Package herdr adapts the Herdr CLI without conflating presentation labels
// with stable pane identity.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
)

type State string

const (
	StateRunning State = "running"
	StateIdle    State = "idle"
	StateHusk    State = "husk"
	StateLost    State = "lost"
)

var (
	ErrSessionMissing = errors.New("Herdr pane is structurally missing")
	ErrSessionHusk    = errors.New("Herdr pane is an agent-less husk without resumable identity")
)

type StartRequest struct {
	TaskID       domain.TaskID
	Attempt      int
	WorktreePath string
	Brief        string
}

type Session struct {
	AgentName      string
	AgentSessionID string
	SessionName    string
	WorkspaceID    string
	TabID          string
	PaneID         string
	WorktreePath   string
}

// Adapter is the worker-runtime boundary used by the control plane. Tests can
// replace it without launching a terminal or nested agent.
type Adapter interface {
	StartCodex(context.Context, StartRequest) (Session, error)
	Observe(context.Context, Session) (State, error)
	Wake(context.Context, Session, string) (Session, error)
}

// Wake prompts a live idle agent in place. A restored agent-less pane is a
// dead husk: Wake creates a replacement tab in the same workspace, resumes
// the persisted Codex session there, verifies the prompt is accepted, and
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
		if strings.TrimSpace(session.AgentName) == "" || !safeAgentSessionID.MatchString(session.AgentSessionID) {
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
	_, stderr, err := a.run(ctx, "agent", "prompt", session.PaneID, message,
		"--wait", "--until", "working", "--timeout", "30000")
	if err != nil {
		return Session{}, commandError("wake Codex", err, stderr)
	}
	return session, nil
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
	command := "codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust resume " + husk.AgentSessionID
	if err := a.launchCodexInPane(ctx, replacement, command, true); err != nil {
		return Session{}, fmt.Errorf("resume Codex in replacement pane: %w", err)
	}
	if _, stderr, err := a.run(ctx, "agent", "prompt", replacement.PaneID, message,
		"--wait", "--until", "working", "--timeout", "30000"); err != nil {
		promptErr := commandError("verify resumed Codex", err, stderr)
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
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" {
		return Session{}, errors.New("Herdr adapter requires an explicit session and runner")
	}
	if in.TaskID == "" || in.Attempt < 1 || strings.TrimSpace(in.WorktreePath) == "" || strings.TrimSpace(in.Brief) == "" {
		return Session{}, errors.New("task, attempt, worktree, and brief are required")
	}
	label := strings.TrimSpace(a.WorkspaceLabel)
	if label == "" {
		label = "Parallel Intellect"
	}
	stdout, stderr, err := a.run(ctx, "workspace", "create", "--cwd", in.WorktreePath, "--label", label, "--no-focus")
	if err != nil {
		return Session{}, commandError("create workspace", err, stderr)
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
		return Session{}, fmt.Errorf("decode Herdr workspace response: %w", err)
	}
	session := Session{
		AgentName: agentName(in.TaskID, in.Attempt), SessionName: a.SessionName,
		WorkspaceID: created.Result.Workspace.ID, TabID: created.Result.Tab.ID, PaneID: created.Result.RootPane.ID,
		WorktreePath: in.WorktreePath,
	}
	if session.WorkspaceID == "" || session.TabID == "" || session.PaneID == "" {
		return Session{}, errors.New("Herdr workspace response omitted stable identifiers")
	}
	if err := a.launchCodexInPane(ctx, session,
		"codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust", true); err != nil {
		return session, fmt.Errorf("start Codex: %w", err)
	}
	promptOutput, stderr, err := a.run(ctx, "agent", "prompt", session.PaneID, in.Brief,
		"--wait", "--until", "working", "--timeout", "30000")
	if err != nil {
		return session, commandError("deliver initial Codex prompt", err, stderr)
	}
	if session.AgentSessionID, err = a.captureAgentSessionID(ctx, session, promptOutput); err != nil {
		return session, err
	}
	return session, nil
}

func (a *CommandAdapter) launchCodexInPane(ctx context.Context, session Session, command string, waitForBanner bool) error {
	_, stderr, err := a.run(ctx, "pane", "run", session.PaneID, command)
	if err != nil {
		return commandError("launch Codex command", err, stderr)
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
			return a.waitForCodexComposer(ctx, session.PaneID)
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
			return errors.New("Codex did not register in the Herdr pane after launch")
		case <-ticker.C:
		}
	}
}

func (a *CommandAdapter) waitForCodexComposer(ctx context.Context, paneID string) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	folderTrustAccepted := false
	hooksDeclined := false
	for {
		visible, err := a.readPane(ctx, paneID)
		if err != nil {
			return fmt.Errorf("wait for Codex composer: %w", err)
		}
		if strings.Contains(visible, "OpenAI Codex") {
			return nil
		}
		switch {
		case isCodexFolderTrustScreen(visible):
			if !folderTrustAccepted {
				if err := a.sendKeys(ctx, paneID, "enter"); err != nil {
					return fmt.Errorf("accept Codex folder trust: %w", err)
				}
				folderTrustAccepted = true
			}
		case isCodexHooksTrustScreen(visible):
			if !hooksDeclined {
				// Product workers never trust project hooks. The Codex prompt
				// selects this option with Down, Down, Enter.
				if err := a.sendKeys(ctx, paneID, "down", "down", "enter"); err != nil {
					return fmt.Errorf("decline Codex hooks trust: %w", err)
				}
				hooksDeclined = true
			}
		case strings.TrimSpace(visible) != "":
			return fmt.Errorf("Codex composer did not appear; visible pane:\n%s", visible)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("Codex composer did not appear; visible pane:\n%s", visible)
		case <-ticker.C:
		}
	}
}

func isCodexFolderTrustScreen(visible string) bool {
	return strings.Contains(visible, "Do you trust the contents of this directory?") &&
		strings.Contains(visible, "Yes, continue")
}

func isCodexHooksTrustScreen(visible string) bool {
	return strings.Contains(strings.ToLower(visible), "hooks") &&
		strings.Contains(visible, "Continue without trusting")
}

func (a *CommandAdapter) sendKeys(ctx context.Context, paneID string, keys ...string) error {
	args := append([]string{"pane", "send-keys", paneID}, keys...)
	_, stderr, err := a.run(ctx, args...)
	if err != nil {
		return commandError("send keys to Codex", err, stderr)
	}
	return nil
}

func (a *CommandAdapter) Observe(ctx context.Context, session Session) (State, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" || session.PaneID == "" {
		return "", errors.New("Herdr observation requires an explicit session and pane")
	}
	if session.SessionName != "" && session.SessionName != a.SessionName {
		return "", errors.New("Herdr observation session identity mismatch")
	}
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
			return "", commandError("probe Codex pane", runErr, stderr)
		}
		return "", fmt.Errorf("decode Herdr pane response: %w", err)
	}
	if paneResponse.Error.Code == "pane_not_found" {
		return StateLost, nil
	}
	if paneResponse.Error.Code != "" {
		return "", fmt.Errorf("probe Codex pane: Herdr error %s", paneResponse.Error.Code)
	}
	if runErr != nil {
		return "", commandError("probe Codex pane", runErr, stderr)
	}
	if paneResponse.Result.Pane.PaneID != session.PaneID {
		return "", errors.New("Herdr pane response did not preserve pane identity")
	}

	stdout, stderr, runErr = a.run(ctx, "agent", "get", session.PaneID)
	body = stdout
	if len(bytes.TrimSpace(body)) == 0 {
		body = stderr
	}
	var response struct {
		Result struct {
			Agent struct {
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
			return "", commandError("observe Codex", runErr, stderr)
		}
		return "", fmt.Errorf("decode Herdr agent response: %w", err)
	}
	switch response.Error.Code {
	case "agent_not_found":
		return StateHusk, nil
	case "pane_not_found":
		return StateLost, nil
	case "":
	default:
		return "", fmt.Errorf("observe Codex: Herdr error %s", response.Error.Code)
	}
	if runErr != nil {
		return "", commandError("observe Codex", runErr, stderr)
	}
	if response.Result.Agent.PaneID != session.PaneID {
		return "", errors.New("Herdr agent response did not preserve pane identity")
	}
	if response.Result.Agent.StateChangeSeq < 1 {
		// Herdr 0.7.3 may restore the last agent/session presentation with
		// sequence zero even though the harness process and live registration
		// are gone. This is a husk, not a promptable idle agent.
		return StateHusk, nil
	}
	switch response.Result.Agent.Status {
	case "working":
		return StateRunning, nil
	case "idle", "done", "blocked":
		return StateIdle, nil
	default:
		return "", fmt.Errorf("unknown Herdr agent status %q", response.Result.Agent.Status)
	}
}

func (a *CommandAdapter) captureAgentSessionID(ctx context.Context, session Session, promptResponse []byte) (string, error) {
	if value, err := agentSessionID(promptResponse, session.PaneID); err != nil {
		return "", fmt.Errorf("decode initial Codex session identity: %w", err)
	} else if value != "" {
		return value, nil
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		stdout, stderr, runErr := a.run(ctx, "agent", "get", session.PaneID)
		if runErr != nil {
			return "", commandError("capture Codex session identity", runErr, stderr)
		}
		value, err := agentSessionID(stdout, session.PaneID)
		if err != nil {
			return "", fmt.Errorf("decode Codex session identity: %w", err)
		}
		if value != "" {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("Herdr did not report the Codex session identity after launch")
		case <-ticker.C:
		}
	}
}

func agentSessionID(data []byte, paneID string) (string, error) {
	var response struct {
		Result struct {
			Agent struct {
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
	return strings.TrimSpace(response.Result.Agent.AgentSession.Value), nil
}

func (a *CommandAdapter) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	// Herdr 0.7.x requires the explicit session flag on every call. It is
	// deliberately appended last because environment-only routing is unsafe.
	return a.runner.Run(ctx, append(args, "--session", a.SessionName)...)
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var safeAgentSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func agentName(taskID domain.TaskID, attempt int) string {
	name := unsafeName.ReplaceAllString(string(taskID), "-")
	return fmt.Sprintf("pi-%s-a%d", name, attempt)
}

func commandError(operation string, err error, stderr []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		return fmt.Errorf("Herdr %s: %w", operation, err)
	}
	return fmt.Errorf("Herdr %s: %w: %s", operation, err, detail)
}
