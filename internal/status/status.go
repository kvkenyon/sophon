// Package status builds the deterministic, read-only /status snapshot.
package status

import (
	"context"
	"fmt"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/signals"
)

// Snapshot is the complete current mission view. Its four section fields map
// exactly to the commander /status contract.
type Snapshot struct {
	Mission            *domain.Mission    `json:"mission,omitempty"`
	NeedsYourAttention NeedsYourAttention `json:"needs_your_attention"`
	RecentlyCompleted  []Task             `json:"recently_completed"`
	Underway           []Task             `json:"underway"`
	UpNext             []Task             `json:"up_next"`
}

type NeedsYourAttention struct {
	Tasks   []Task           `json:"tasks"`
	Signals []signals.Signal `json:"signals"`
}

// Task includes the current attempt's recorded worker session when one exists.
type Task struct {
	domain.Task
	WorkerSession *domain.WorkerSession `json:"worker_session,omitempty"`
}

type Store interface {
	Mission(context.Context, domain.MissionID) (domain.Mission, error)
	Tasks(context.Context, domain.MissionID) ([]domain.Task, error)
	Signals(context.Context, db.ListSignalsFilter) ([]signals.Signal, error)
	WorkerSessions(context.Context, domain.MissionID) ([]domain.WorkerSession, error)
}

func Load(ctx context.Context, store Store, missionID domain.MissionID) (Snapshot, error) {
	mission, err := store.Mission(ctx, missionID)
	if err != nil {
		return Snapshot{}, err
	}
	tasks, err := store.Tasks(ctx, missionID)
	if err != nil {
		return Snapshot{}, err
	}
	openSignals, err := store.Signals(ctx, db.ListSignalsFilter{MissionID: missionID, Status: signals.SignalOpen})
	if err != nil {
		return Snapshot{}, err
	}
	sessions, err := store.WorkerSessions(ctx, missionID)
	if err != nil {
		return Snapshot{}, err
	}

	byTaskAttempt := make(map[string]domain.WorkerSession, len(sessions))
	for _, session := range sessions {
		byTaskAttempt[fmt.Sprintf("%s/%d", session.TaskID, session.Attempt)] = session
	}
	snapshot := Snapshot{Mission: &mission, NeedsYourAttention: NeedsYourAttention{Tasks: []Task{}, Signals: openSignals}, RecentlyCompleted: []Task{}, Underway: []Task{}, UpNext: []Task{}}
	for _, raw := range tasks {
		item := Task{Task: raw}
		if session, ok := byTaskAttempt[fmt.Sprintf("%s/%d", raw.ID, raw.CurrentAttempt)]; ok {
			item.WorkerSession = &session
		}
		switch raw.State {
		case domain.TaskBlocked, domain.TaskDeliveryBlocked, domain.TaskNeedsAttention, domain.TaskFailed:
			snapshot.NeedsYourAttention.Tasks = append(snapshot.NeedsYourAttention.Tasks, item)
		case domain.TaskReady, domain.TaskReportReady, domain.TaskDelivered, domain.TaskDeliveredBranch, domain.TaskCancelled:
			snapshot.RecentlyCompleted = append(snapshot.RecentlyCompleted, item)
		case domain.TaskProvisioning, domain.TaskStarting, domain.TaskRunning, domain.TaskCollecting, domain.TaskValidating, domain.TaskCancelling:
			snapshot.Underway = append(snapshot.Underway, item)
		case domain.TaskQueued:
			snapshot.UpNext = append(snapshot.UpNext, item)
		}
	}
	return snapshot, nil
}

// Empty returns the valid snapshot for a store with no selected mission.
func Empty() Snapshot {
	return Snapshot{NeedsYourAttention: NeedsYourAttention{Tasks: []Task{}, Signals: []signals.Signal{}}, RecentlyCompleted: []Task{}, Underway: []Task{}, UpNext: []Task{}}
}
