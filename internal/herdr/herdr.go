// Package herdr adapts the Herdr CLI without conflating presentation labels
// with stable pane identity.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"parallel-intellect/internal/domain"
)

type State string

const (
	StateRunning State = "running"
	StateIdle    State = "idle"
	StateLost    State = "lost"
)

type StartRequest struct {
	TaskID       domain.TaskID
	Attempt      int
	WorktreePath string
	Brief        string
}

type Session struct {
	AgentName   string
	SessionName string
	WorkspaceID string
	TabID       string
	PaneID      string
}

// Adapter is the worker-runtime boundary used by the control plane. Tests can
// replace it without launching a terminal or nested agent.
type Adapter interface {
	StartCodex(context.Context, StartRequest) (Session, error)
	Observe(context.Context, Session) (State, error)
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
	}
	if session.WorkspaceID == "" || session.TabID == "" || session.PaneID == "" {
		return Session{}, errors.New("Herdr workspace response omitted stable identifiers")
	}
	_, stderr, err = a.run(ctx, "agent", "start", session.AgentName, "--kind", "codex", "--pane", session.PaneID)
	if err != nil {
		return session, commandError("start Codex", err, stderr)
	}
	_, stderr, err = a.run(ctx, "agent", "prompt", session.PaneID, in.Brief)
	if err != nil {
		return session, commandError("deliver initial Codex prompt", err, stderr)
	}
	return session, nil
}

func (a *CommandAdapter) Observe(ctx context.Context, session Session) (State, error) {
	if a == nil || a.runner == nil || strings.TrimSpace(a.SessionName) == "" || session.PaneID == "" {
		return "", errors.New("Herdr observation requires an explicit session and pane")
	}
	if session.SessionName != "" && session.SessionName != a.SessionName {
		return "", errors.New("Herdr observation session identity mismatch")
	}
	stdout, stderr, runErr := a.run(ctx, "agent", "get", session.PaneID)
	var response struct {
		Result struct {
			Agent struct {
				PaneID string `json:"pane_id"`
				Status string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		if runErr != nil {
			return "", commandError("observe Codex", runErr, stderr)
		}
		return "", fmt.Errorf("decode Herdr agent response: %w", err)
	}
	switch response.Error.Code {
	case "agent_not_found", "pane_not_found":
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
	switch response.Result.Agent.Status {
	case "working":
		return StateRunning, nil
	case "idle", "done", "blocked":
		return StateIdle, nil
	default:
		return "", fmt.Errorf("unknown Herdr agent status %q", response.Result.Agent.Status)
	}
}

func (a *CommandAdapter) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	// Herdr 0.7.x requires the explicit session flag on every call. It is
	// deliberately appended last because environment-only routing is unsafe.
	return a.runner.Run(ctx, append(args, "--session", a.SessionName)...)
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

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
