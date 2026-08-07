package commander

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sophon/internal/db"
	"sophon/internal/domain"
)

var relevantWakeEvents = map[string]struct{}{
	"task.ready": {}, "task.blocked": {}, "task.delivered": {}, "task.delivered_branch": {},
	"worker.blocked": {}, "worker.completed": {}, "signal.created": {},
}

type EventWaker struct {
	Store   *db.Store
	Runtime Adapter
}

type WakeEnvelope struct {
	Kind      string           `json:"kind"`
	MissionID domain.MissionID `json:"mission_id"`
	Events    []domain.Event   `json:"events"`
}

// Wake delivers all new mission-relevant events as one bounded structured
// follow-up and advances the durable cursor only after Herdr accepts it.
func (w *EventWaker) Wake(ctx context.Context, missionID domain.MissionID) (domain.CommanderSession, error) {
	if w == nil || w.Store == nil || w.Runtime == nil {
		return domain.CommanderSession{}, errors.New("commander event waker is not fully configured")
	}
	persisted, err := w.Store.CommanderSession(ctx, missionID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if persisted.State == domain.CommanderSessionStopped || persisted.State == domain.CommanderSessionFailed || persisted.State == domain.CommanderSessionNeedsAttention {
		return persisted, nil
	}
	all, err := w.Store.MissionEventsAfter(ctx, missionID, persisted.LastEventSequence)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	observedThrough := persisted.LastEventSequence
	relevant := make([]domain.Event, 0)
	for _, event := range all {
		observedThrough = event.Sequence
		if _, ok := relevantWakeEvents[event.Type]; ok {
			relevant = append(relevant, event)
		}
	}
	if observedThrough == persisted.LastEventSequence {
		return persisted, nil
	}
	if len(relevant) > 0 {
		budgetCommand, err := newCommandID()
		if err != nil {
			return domain.CommanderSession{}, err
		}
		persisted, err = w.Store.ReserveCommanderTurn(ctx, budgetCommand, db.ReserveCommanderTurnInput{
			MissionID: missionID, SessionID: persisted.ID, ExpectedVersion: persisted.Version, Actor: "event-router",
		})
		if err != nil {
			return domain.CommanderSession{}, err
		}
		if persisted.State == domain.CommanderSessionNeedsAttention {
			signalCommand, signalErr := newCommandID()
			if signalErr != nil {
				return domain.CommanderSession{}, signalErr
			}
			if _, signalErr = w.Store.EnsureCommanderBudgetSignal(ctx, signalCommand, persisted.ID); signalErr != nil {
				return domain.CommanderSession{}, signalErr
			}
			return persisted, db.ErrBudgetExhausted
		}
		body, err := json.Marshal(WakeEnvelope{Kind: "mission_events", MissionID: missionID, Events: relevant})
		if err != nil {
			return domain.CommanderSession{}, err
		}
		snapshot, err := w.Store.CommanderLaunchContext(ctx, missionID)
		if err != nil {
			return domain.CommanderSession{}, err
		}
		delivered, err := w.Runtime.FollowUp(ctx, runtimeSession(persisted, snapshot.ProjectPath), string(body))
		if err != nil {
			return domain.CommanderSession{}, fmt.Errorf("wake commander for mission events: %w", err)
		}
		placement := changedPlacement(persisted, delivered)
		if placement != nil || persisted.State != domain.CommanderSessionRunning {
			commandID, err := newCommandID()
			if err != nil {
				return domain.CommanderSession{}, err
			}
			persisted, err = w.Store.ObserveCommanderSession(ctx, commandID, db.ObserveCommanderSessionInput{
				SessionID: persisted.ID, MissionID: missionID, ExpectedState: persisted.State,
				ExpectedVersion: persisted.Version, ObservedState: domain.CommanderSessionRunning,
				Placement: placement, Actor: "event-router",
			})
			if err != nil {
				return domain.CommanderSession{}, err
			}
		}
	}
	commandID, err := newCommandID()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return w.Store.RecordCommanderWake(ctx, commandID, db.RecordCommanderWakeInput{
		SessionID: persisted.ID, MissionID: missionID, ExpectedVersion: persisted.Version,
		ObservedThrough: observedThrough, Delivered: relevant, Actor: "event-router",
	})
}
