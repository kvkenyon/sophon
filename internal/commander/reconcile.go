package commander

import (
	"context"
	"errors"
	"fmt"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
)

const restartWake = `{"kind":"commander_session_resumed","reason":"Herdr restart restored an agent-less pane","action":"Reconcile the current structured mission state before acting."}`

type Reconciler struct {
	Store   *db.Store
	Runtime Adapter
}

func (r *Reconciler) Reconcile(ctx context.Context, missionID domain.MissionID) (domain.CommanderSession, error) {
	if r == nil || r.Store == nil || r.Runtime == nil {
		return domain.CommanderSession{}, errors.New("commander reconciler is not fully configured")
	}
	persisted, err := r.Store.CommanderSession(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if persisted.State == domain.CommanderSessionStopped || persisted.State == domain.CommanderSessionFailed {
		return persisted, nil
	}
	snapshot, err := r.Store.CommanderLaunchContext(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	session := runtimeSession(persisted, snapshot.ProjectPath)
	observed, err := r.Runtime.State(ctx, session)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	var state domain.CommanderSessionState
	var reason string
	var placement *db.CommanderSessionPlacement
	switch observed {
	case StateRunning:
		state = domain.CommanderSessionRunning
	case StateIdle:
		state = domain.CommanderSessionIdle
	case StateHusk:
		resumed, err := r.Runtime.FollowUp(ctx, session, restartWake)
		if err != nil {
			return domain.CommanderSession{}, fmt.Errorf("resume commander husk: %w", err)
		}
		state = domain.CommanderSessionRunning
		placement = changedPlacement(persisted, resumed)
	case StateMissing:
		state = domain.CommanderSessionNeedsAttention
		reason = "expected Herdr commander pane is missing"
	default:
		return domain.CommanderSession{}, fmt.Errorf("unknown commander observation %q", observed)
	}
	if state == persisted.State && placement == nil && persisted.LastObservedAt != nil {
		return persisted, nil
	}
	commandID, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return r.Store.ObserveCommanderSession(ctx, commandID, db.ObserveCommanderSessionInput{
		SessionID: persisted.ID, MissionID: missionID, ExpectedState: persisted.State,
		ExpectedVersion: persisted.Version, ObservedState: state, FailureReason: reason,
		Placement: placement, Actor: "reconciler",
	})
}
