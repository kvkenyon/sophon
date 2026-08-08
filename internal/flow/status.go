package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/store"
	"sophon/internal/treehouse"
)

// ReleaseLease conditionally returns the current attempt's lease by exact
// recorded identity and publishes release.json. Re-running returns the
// existing receipt without touching Treehouse.
func (f *Flow) ReleaseLease(ctx context.Context, taskID string) (store.Release, error) {
	if f.deps.Leases == nil {
		return store.Release{}, errors.New("flow is not fully configured for lease release")
	}
	release, err := store.Acquire(ctx, "release "+taskID)
	if err != nil {
		return store.Release{}, err
	}
	defer release()
	task, mission, err := f.taskAndMission(taskID)
	if err != nil {
		return store.Release{}, err
	}
	attempt, err := currentAttempt(task)
	if err != nil {
		return store.Release{}, err
	}
	if existing, err := store.ReadRelease(task.MissionID, taskID, attempt); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Release{}, err
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Release{}, err
	}
	// The release is conditional on exact lease id and holder; any failure is
	// a fence and publishes nothing.
	if err := f.deps.Leases.Release(ctx, mission.ProjectPath, treehouse.Allocation{
		WorktreePath: spawn.WorktreePath, LeaseID: spawn.LeaseID, LeaseHolder: spawn.LeaseHolder}); err != nil {
		return store.Release{}, fmt.Errorf("release lease %s/%s for attempt %d: %w",
			spawn.LeaseID, spawn.LeaseHolder, attempt, err)
	}
	record := store.Release{TaskID: taskID, Attempt: attempt, LeaseID: spawn.LeaseID,
		LeaseHolder: spawn.LeaseHolder, ReleasedAt: time.Now().UTC()}
	homeDir, err := datahome.Dir()
	if err != nil {
		return store.Release{}, err
	}
	if err := store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "release.json"), record); err != nil {
		return store.Release{}, fmt.Errorf("publish release receipt: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("released: attempt %d lease returned", attempt))
	return record, nil
}

// MissionStatus rolls one mission up with its derived task states.
type MissionStatus struct {
	Mission store.Mission      `json:"mission"`
	Tasks   []store.TaskStatus `json:"tasks"`
}

// Report is the full read-time status across every mission.
type Report struct {
	Missions []MissionStatus `json:"missions"`
}

// Status derives every task's state from records and augments active tasks
// with live pane observation. It takes no lock and never reads wake lines.
func (f *Flow) Status(ctx context.Context) (Report, error) {
	missions, err := store.ListMissions()
	if err != nil {
		return Report{}, err
	}
	report := Report{Missions: make([]MissionStatus, 0, len(missions))}
	for _, mission := range missions {
		entry := MissionStatus{Mission: mission}
		tasks, err := store.ListTasks(mission.ID)
		if err != nil {
			return Report{}, err
		}
		for _, task := range tasks {
			status, err := store.Derive(task)
			if err != nil {
				return Report{}, err
			}
			if status.State == store.StateActive {
				status = f.augmentActive(ctx, status)
			}
			entry.Tasks = append(entry.Tasks, status)
		}
		report.Missions = append(report.Missions, entry)
	}
	return report, nil
}

// augmentActive observes the current attempt's pane live; adapter failures
// degrade to unknown-pane rather than an error.
func (f *Flow) augmentActive(ctx context.Context, status store.TaskStatus) store.TaskStatus {
	if f.deps.Panes == nil {
		return status
	}
	spawn, err := store.ReadSpawn(status.Task.MissionID, status.Task.ID, status.Attempt)
	if err != nil {
		// A bumped attempt whose spawn never completed has no pane to observe.
		status.Detail = "no spawn receipt for current attempt"
		return status
	}
	state, err := f.deps.Panes.Observe(ctx, spawn.Pane)
	if err != nil {
		status.State = "unknown-pane"
		return status
	}
	status.State = string(state)
	return status
}

// Send wakes the current attempt's worker pane with a message and persists
// the (possibly replaced) pane placement back into spawn.json.
func (f *Flow) Send(ctx context.Context, taskID, message string) error {
	if f.deps.Panes == nil {
		return errors.New("flow is not fully configured for send")
	}
	if err := requireNonEmpty(taskID, message); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	release, err := store.Acquire(ctx, "send "+taskID)
	if err != nil {
		return err
	}
	defer release()
	task, err := store.FindTask(taskID)
	if err != nil {
		return err
	}
	attempt, err := currentAttempt(task)
	if err != nil {
		return err
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return err
	}
	session, err := f.deps.Panes.Wake(ctx, spawn.Pane, message)
	if err != nil {
		return fmt.Errorf("wake worker pane: %w", err)
	}
	spawn.Pane = session
	homeDir, err := datahome.Dir()
	if err != nil {
		return err
	}
	return store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "spawn.json"), spawn)
}
