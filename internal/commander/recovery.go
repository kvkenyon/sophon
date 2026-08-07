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

const missingPaneWake = `{"kind":"commander_session_resumed","reason":"front door recovered a missing Herdr commander pane","action":"Reconcile the current structured mission state before acting."}`

// Recovery replaces a dead front-door commander. A verified native resume is
// preferred; when it cannot be performed, a fresh commander starts on the
// same persisted mission (or in project intake mode).
type Recovery struct {
	Store        *db.Store
	Runtime      Adapter
	Prompts      PromptComposer
	DatabasePath string
}

func (r *Recovery) RecoverProject(ctx context.Context, projectID domain.ProjectID) (domain.CommanderSession, error) {
	if r == nil || r.Store == nil || r.Runtime == nil || projectID == "" {
		return domain.CommanderSession{}, errors.New("commander recovery is not fully configured")
	}
	dead, err := r.Store.ProjectCommanderSession(ctx, projectID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if dead.State != domain.CommanderSessionNeedsAttention {
		return dead, nil
	}
	project, err := r.Store.Project(ctx, string(projectID))
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if strings.TrimSpace(dead.AgentSessionID) != "" {
		resumed, resumeErr := r.Runtime.Resume(ctx, runtimeSession(dead, project.Path), missingPaneWake)
		if resumeErr == nil {
			return r.retireAndRecord(ctx, dead, resumed)
		}
	}
	if _, err := r.retire(ctx, dead, "commander resume unavailable; starting a fresh commander"); err != nil {
		return domain.CommanderSession{}, err
	}
	starter := Starter{Store: r.Store, Runtime: r.Runtime, Prompts: r.Prompts}
	if dead.MissionID != "" {
		started, err := starter.Start(ctx, StartRequest{MissionID: dead.MissionID, Runtime: herdr.Runtime(dead.Runtime), Model: dead.Model,
			PiExtensionPath: dead.PiExtensionPath, Budget: dead.Budget})
		if err != nil {
			return domain.CommanderSession{}, fmt.Errorf("start fresh mission commander: %w", err)
		}
		return started.Session, nil
	}
	started, err := starter.StartProject(ctx, ProjectStartRequest{ProjectID: projectID, Runtime: herdr.Runtime(dead.Runtime),
		Model: dead.Model, DatabasePath: r.DatabasePath, Budget: dead.Budget})
	if err != nil {
		return domain.CommanderSession{}, fmt.Errorf("start fresh intake commander: %w", err)
	}
	return started.Session, nil
}

func (r *Recovery) retireAndRecord(ctx context.Context, dead domain.CommanderSession, resumed Session) (domain.CommanderSession, error) {
	if _, err := r.retire(ctx, dead, "replacement commander resumed after missing Herdr pane"); err != nil {
		return domain.CommanderSession{}, err
	}
	newID, err := id.New("csn")
	if err != nil {
		return domain.CommanderSession{}, err
	}
	commandID, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return r.Store.RecordCommanderSession(ctx, commandID, db.RecordCommanderSessionInput{ProjectID: dead.ProjectID, MissionID: dead.MissionID, Actor: "recovery",
		Session: domain.CommanderSession{ID: domain.SessionID(newID), Runtime: dead.Runtime, HerdrSessionName: resumed.Herdr.SessionName,
			HerdrWorkspaceID: resumed.Herdr.WorkspaceID, HerdrTabID: resumed.Herdr.TabID, HerdrPaneID: resumed.Herdr.PaneID,
			HerdrAgentName: resumed.Herdr.AgentName, AgentSessionID: resumed.Herdr.AgentSessionID, Model: dead.Model,
			PiExtensionPath: dead.PiExtensionPath, Budget: dead.Budget}})
}

func (r *Recovery) retire(ctx context.Context, dead domain.CommanderSession, reason string) (domain.CommanderSession, error) {
	commandID, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return r.Store.RetireCommanderSession(ctx, commandID, dead.ID, dead.State, dead.Version, "recovery", reason)
}
