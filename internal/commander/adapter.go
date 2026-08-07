package commander

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
)

type State string

const (
	StateRunning State = "running"
	StateIdle    State = "idle"
	StateHusk    State = "husk"
	StateMissing State = "missing"
)

type StartConfig struct {
	SessionID       domain.SessionID
	ProjectID       domain.ProjectID
	MissionID       domain.MissionID
	Runtime         herdr.Runtime
	WorkingDir      string
	Prompt          string
	Model           string
	PiExtensionPath string
}

type Session struct {
	ID        domain.SessionID
	ProjectID domain.ProjectID
	MissionID domain.MissionID
	Runtime   herdr.Runtime
	Herdr     herdr.Session
}

// Adapter is the common commander runtime surface. Pi, Claude, and Codex use
// the same implementation because runtime-specific mechanics remain in the
// M5 Herdr launch profiles.
type Adapter interface {
	Start(context.Context, StartConfig) (Session, error)
	Prompt(context.Context, Session, string) (Session, error)
	Steer(context.Context, Session, string) (Session, error)
	FollowUp(context.Context, Session, string) (Session, error)
	State(context.Context, Session) (State, error)
	Abort(context.Context, Session) error
}

type terminal interface {
	Start(context.Context, herdr.StartRequest) (herdr.Session, error)
	Observe(context.Context, herdr.Session) (herdr.State, error)
	Submit(context.Context, herdr.Session, string) (herdr.Session, error)
	Cancel(context.Context, herdr.Session) error
	Stop(context.Context, herdr.Session) error
}

type HerdrAdapter struct{ Terminal terminal }

func (a HerdrAdapter) Start(ctx context.Context, config StartConfig) (Session, error) {
	if a.Terminal == nil {
		return Session{}, errors.New("commander Herdr terminal is required")
	}
	if config.SessionID == "" || config.ProjectID == "" || strings.TrimSpace(config.WorkingDir) == "" || strings.TrimSpace(config.Prompt) == "" {
		return Session{}, errors.New("commander session, project, working directory, and prompt are required")
	}
	switch config.Runtime {
	case herdr.RuntimeCodex, herdr.RuntimeClaude, herdr.RuntimePi:
	default:
		return Session{}, fmt.Errorf("unsupported commander runtime %q", config.Runtime)
	}
	runtimeSession, err := a.Terminal.Start(ctx, herdr.StartRequest{
		AgentName: "pi-commander-" + string(config.ProjectID), Attempt: 1,
		WorktreePath: config.WorkingDir, Brief: config.Prompt, Runtime: config.Runtime,
		Model: config.Model, PiExtensionPath: config.PiExtensionPath,
	})
	if err != nil {
		return Session{}, err
	}
	return Session{ID: config.SessionID, ProjectID: config.ProjectID, MissionID: config.MissionID, Runtime: config.Runtime, Herdr: runtimeSession}, nil
}

func (a HerdrAdapter) Prompt(ctx context.Context, session Session, message string) (Session, error) {
	return a.submit(ctx, session, message)
}

func (a HerdrAdapter) Steer(ctx context.Context, session Session, message string) (Session, error) {
	return a.submit(ctx, session, message)
}

func (a HerdrAdapter) FollowUp(ctx context.Context, session Session, message string) (Session, error) {
	return a.submit(ctx, session, message)
}

func (a HerdrAdapter) submit(ctx context.Context, session Session, message string) (Session, error) {
	if a.Terminal == nil || strings.TrimSpace(message) == "" {
		return Session{}, errors.New("commander terminal and message are required")
	}
	updated, err := a.Terminal.Submit(ctx, session.Herdr, message)
	if err != nil {
		return Session{}, err
	}
	session.Herdr = updated
	return session, nil
}

func (a HerdrAdapter) State(ctx context.Context, session Session) (State, error) {
	if a.Terminal == nil {
		return "", errors.New("commander Herdr terminal is required")
	}
	state, err := a.Terminal.Observe(ctx, session.Herdr)
	if err != nil {
		return "", err
	}
	switch state {
	case herdr.StateRunning:
		return StateRunning, nil
	case herdr.StateIdle:
		return StateIdle, nil
	case herdr.StateHusk:
		return StateHusk, nil
	case herdr.StateLost:
		return StateMissing, nil
	default:
		return "", fmt.Errorf("unsupported Herdr commander state %q", state)
	}
}

func (a HerdrAdapter) Abort(ctx context.Context, session Session) error {
	if a.Terminal == nil {
		return errors.New("commander Herdr terminal is required")
	}
	state, err := a.Terminal.Observe(ctx, session.Herdr)
	if err != nil {
		return err
	}
	if state == herdr.StateRunning {
		if err := a.Terminal.Cancel(ctx, session.Herdr); err != nil {
			return err
		}
	}
	if state == herdr.StateLost {
		return herdr.ErrSessionMissing
	}
	return a.Terminal.Stop(ctx, session.Herdr)
}

func runtimeSession(session domain.CommanderSession, workingDir string) Session {
	runtime := herdr.Runtime(session.Runtime)
	return Session{ID: session.ID, ProjectID: session.ProjectID, MissionID: session.MissionID, Runtime: runtime, Herdr: herdr.Session{
		Runtime: runtime, AgentName: session.HerdrAgentName, AgentSessionID: session.AgentSessionID,
		SessionName: session.HerdrSessionName, WorkspaceID: session.HerdrWorkspaceID,
		TabID: session.HerdrTabID, PaneID: session.HerdrPaneID, WorktreePath: workingDir,
		Model: session.Model, PiExtensionPath: session.PiExtensionPath,
	}}
}
