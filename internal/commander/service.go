package commander

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
	"parallel-intellect/internal/id"
)

type Starter struct {
	Store   *db.Store
	Runtime Adapter
	Prompts PromptComposer
}

type StartRequest struct {
	MissionID       domain.MissionID
	Runtime         herdr.Runtime
	Model           string
	PiExtensionPath string
	Budget          domain.CommanderBudget
}

type ProjectStartRequest struct {
	ProjectID    domain.ProjectID
	Runtime      herdr.Runtime
	Model        string
	DatabasePath string
	Budget       domain.CommanderBudget
}

type StartResult struct {
	Session domain.CommanderSession `json:"commander_session"`
	Prompt  string                  `json:"-"`
}

func (s *Starter) Start(ctx context.Context, in StartRequest) (StartResult, error) {
	if s == nil || s.Store == nil || s.Runtime == nil || in.MissionID == "" {
		return StartResult{}, errors.New("commander starter is not fully configured")
	}
	snapshot, err := s.Store.CommanderLaunchContext(ctx, in.MissionID)
	if err != nil {
		return StartResult{}, err
	}
	if snapshot.Mission.CommanderSessionID != "" {
		return StartResult{}, errors.New("mission already has a commander session")
	}
	if _, err := s.Store.ProjectCommanderSession(ctx, snapshot.Mission.ProjectID); err == nil {
		return StartResult{}, errors.New("project already has a commander session")
	} else if !errors.Is(err, db.ErrNotFound) {
		return StartResult{}, err
	}
	rawSessionID, err := id.New("csn")
	if err != nil {
		return StartResult{}, err
	}
	sessionID := domain.SessionID(rawSessionID)
	skillDir, err := s.Prompts.MaterializeSkills(sessionID)
	if err != nil {
		return StartResult{}, err
	}
	prompt, err := s.Prompts.ComposeWithSkills(snapshot, skillDir)
	if err != nil {
		return StartResult{}, err
	}
	runtimeSession, err := s.Runtime.Start(ctx, StartConfig{
		SessionID: sessionID, ProjectID: snapshot.Mission.ProjectID,
		MissionID: in.MissionID, Runtime: in.Runtime,
		WorkingDir: snapshot.ProjectPath, Prompt: prompt, Model: in.Model, PiExtensionPath: in.PiExtensionPath,
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("launch commander: %w", err)
	}
	commandID, err := newCommandID()
	if err != nil {
		return StartResult{}, err
	}
	recorded, err := s.Store.RecordCommanderSession(ctx, commandID, db.RecordCommanderSessionInput{
		MissionID: in.MissionID, Actor: "operator", Session: domain.CommanderSession{
			ID: runtimeSession.ID, Runtime: string(in.Runtime), HerdrSessionName: runtimeSession.Herdr.SessionName,
			HerdrWorkspaceID: runtimeSession.Herdr.WorkspaceID, HerdrTabID: runtimeSession.Herdr.TabID,
			HerdrPaneID: runtimeSession.Herdr.PaneID, HerdrAgentName: runtimeSession.Herdr.AgentName,
			AgentSessionID: runtimeSession.Herdr.AgentSessionID, Model: in.Model, PiExtensionPath: in.PiExtensionPath,
			Budget: in.Budget,
		},
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("persist commander session: %w", err)
	}
	return StartResult{Session: recorded, Prompt: prompt}, nil
}

// StartProject launches a persistent commander directly into conversational
// intake. A mission is deliberately absent until the operator describes work.
func (s *Starter) StartProject(ctx context.Context, in ProjectStartRequest) (StartResult, error) {
	if s == nil || s.Store == nil || s.Runtime == nil || in.ProjectID == "" {
		return StartResult{}, errors.New("project commander starter is not fully configured")
	}
	project, err := s.Store.Project(ctx, string(in.ProjectID))
	if err != nil {
		return StartResult{}, err
	}
	if _, err := s.Store.ProjectCommanderSession(ctx, in.ProjectID); err == nil {
		return StartResult{}, errors.New("project already has a commander session")
	} else if !errors.Is(err, db.ErrNotFound) {
		return StartResult{}, err
	}
	rawSessionID, err := id.New("csn")
	if err != nil {
		return StartResult{}, err
	}
	sessionID := domain.SessionID(rawSessionID)
	skillDir, err := s.Prompts.MaterializeSkills(sessionID)
	if err != nil {
		return StartResult{}, err
	}
	prompt, err := s.Prompts.ComposeIntakeWithSkills(project, in.DatabasePath, skillDir)
	if err != nil {
		return StartResult{}, err
	}
	runtimeSession, err := s.Runtime.Start(ctx, StartConfig{
		SessionID: sessionID, ProjectID: in.ProjectID, Runtime: in.Runtime,
		WorkingDir: project.Path, Prompt: prompt, Model: in.Model,
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("launch intake commander: %w", err)
	}
	commandID, err := newCommandID()
	if err != nil {
		return StartResult{}, err
	}
	recorded, err := s.Store.RecordCommanderSession(ctx, commandID, db.RecordCommanderSessionInput{
		ProjectID: in.ProjectID, Actor: "operator", Session: domain.CommanderSession{
			ID: runtimeSession.ID, Runtime: string(in.Runtime), HerdrSessionName: runtimeSession.Herdr.SessionName,
			HerdrWorkspaceID: runtimeSession.Herdr.WorkspaceID, HerdrTabID: runtimeSession.Herdr.TabID,
			HerdrPaneID: runtimeSession.Herdr.PaneID, HerdrAgentName: runtimeSession.Herdr.AgentName,
			AgentSessionID: runtimeSession.Herdr.AgentSessionID, Model: in.Model, Budget: in.Budget,
		},
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("persist intake commander session: %w", err)
	}
	return StartResult{Session: recorded, Prompt: prompt}, nil
}

type MessageKind string

const (
	MessagePrompt   MessageKind = "prompt"
	MessageSteer    MessageKind = "steer"
	MessageFollowUp MessageKind = "follow_up"
)

type Controller struct {
	Store   *db.Store
	Runtime Adapter
}

func (c *Controller) Send(ctx context.Context, missionID domain.MissionID, kind MessageKind, message string) (domain.CommanderSession, error) {
	if c == nil || c.Store == nil || c.Runtime == nil || missionID == "" || strings.TrimSpace(message) == "" {
		return domain.CommanderSession{}, errors.New("commander controller, mission, and message are required")
	}
	persisted, err := c.Store.CommanderSession(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if persisted.State == domain.CommanderSessionStopped || persisted.State == domain.CommanderSessionFailed {
		return domain.CommanderSession{}, fmt.Errorf("commander session is %s", persisted.State)
	}
	budgetCommand, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	persisted, err = c.Store.ReserveCommanderTurn(ctx, budgetCommand, db.ReserveCommanderTurnInput{
		MissionID: missionID, SessionID: persisted.ID, ExpectedVersion: persisted.Version, Actor: "operator",
	})
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if persisted.State == domain.CommanderSessionNeedsAttention {
		return persisted, db.ErrBudgetExhausted
	}
	messageCommand, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	// Persist before Herdr delivery. A failed or killed pane must not be able
	// to erase an operator instruction that was already accepted by the CLI.
	if _, err := c.Store.RecordCommanderMessage(ctx, messageCommand, db.RecordCommanderMessageInput{
		SessionID: persisted.ID, MissionID: missionID, Kind: string(kind), Message: message, Actor: "operator",
	}); err != nil {
		return domain.CommanderSession{}, err
	}
	snapshot, err := c.Store.CommanderLaunchContext(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	session := runtimeSession(persisted, snapshot.ProjectPath)
	var delivered Session
	switch kind {
	case MessagePrompt:
		delivered, err = c.Runtime.Prompt(ctx, session, message)
	case MessageSteer:
		delivered, err = c.Runtime.Steer(ctx, session, message)
	case MessageFollowUp:
		delivered, err = c.Runtime.FollowUp(ctx, session, message)
	default:
		return domain.CommanderSession{}, fmt.Errorf("unknown commander message kind %q", kind)
	}
	if err != nil {
		return domain.CommanderSession{}, err
	}
	placement := changedPlacement(persisted, delivered)
	commandID, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	updated, err := c.Store.ObserveCommanderSession(ctx, commandID, db.ObserveCommanderSessionInput{
		SessionID: persisted.ID, MissionID: missionID, ExpectedState: persisted.State,
		ExpectedVersion: persisted.Version, ObservedState: domain.CommanderSessionRunning,
		Placement: placement, Actor: "operator",
	})
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return updated, nil
}

func (c *Controller) Abort(ctx context.Context, missionID domain.MissionID) (domain.CommanderSession, error) {
	if c == nil || c.Store == nil || c.Runtime == nil || missionID == "" {
		return domain.CommanderSession{}, errors.New("commander controller and mission are required")
	}
	persisted, err := c.Store.CommanderSession(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if persisted.State == domain.CommanderSessionStopped {
		return persisted, nil
	}
	snapshot, err := c.Store.CommanderLaunchContext(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if err := c.Runtime.Abort(ctx, runtimeSession(persisted, snapshot.ProjectPath)); err != nil {
		return domain.CommanderSession{}, err
	}
	commandID, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	stopping, err := c.Store.ObserveCommanderSession(ctx, commandID, db.ObserveCommanderSessionInput{
		SessionID: persisted.ID, MissionID: missionID, ExpectedState: persisted.State,
		ExpectedVersion: persisted.Version, ObservedState: domain.CommanderSessionStopping, Actor: "operator",
	})
	if err != nil {
		return domain.CommanderSession{}, err
	}
	commandID, err = newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return c.Store.ObserveCommanderSession(ctx, commandID, db.ObserveCommanderSessionInput{
		SessionID: stopping.ID, MissionID: missionID, ExpectedState: stopping.State,
		ExpectedVersion: stopping.Version, ObservedState: domain.CommanderSessionStopped, Actor: "operator",
	})
}

func newCommandID() (domain.CommandID, error) {
	raw, err := id.New("cmd")
	return domain.CommandID(raw), err
}

func changedPlacement(persisted domain.CommanderSession, delivered Session) *db.CommanderSessionPlacement {
	if delivered.Herdr.WorkspaceID == persisted.HerdrWorkspaceID && delivered.Herdr.TabID == persisted.HerdrTabID && delivered.Herdr.PaneID == persisted.HerdrPaneID {
		return nil
	}
	return &db.CommanderSessionPlacement{HerdrWorkspaceID: delivered.Herdr.WorkspaceID,
		HerdrTabID: delivered.Herdr.TabID, HerdrPaneID: delivered.Herdr.PaneID}
}
