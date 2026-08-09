package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// Action kinds a commander can execute deterministically, derived from
// records alone. Verification and validation are commander-owned routine
// work; delivery decisions and recovery judgment never appear here.
const (
	ActionVerifyComplete = "verify-complete"
	ActionValidate       = "validate"
)

// Action is one currently authorized deterministic commander action with the
// exact command that performs it. The list is an action queue: a commander
// drains every entry, re-derives, and repeats until none remain before it
// reports or waits.
type Action struct {
	TaskID  string `json:"task_id"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
}

// Report is the full read-time status across every mission plus the derived
// action queue. The queue is truth from the same records, never a hint.
type Report struct {
	Missions []MissionStatus `json:"missions"`
	Actions  []Action        `json:"actions"`
}

// Status derives every task's state from records and augments active tasks
// with live pane observation. It takes no lock and never reads wake lines.
// It also derives the commander action queue: every ready task yields an
// exact verify-complete action and every verified task whose configured
// validation has no receipt yet yields an exact validate action — verify
// actions first, then validate actions. An existing validation receipt
// (pass or fail) is terminal for the queue: a failure needs commander
// judgment (correction routing), never a blind re-run.
func (f *Flow) Status(ctx context.Context) (Report, error) {
	missions, err := store.ListMissions()
	if err != nil {
		return Report{}, err
	}
	report := Report{Missions: make([]MissionStatus, 0, len(missions))}
	var verify, validate []Action
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
			switch {
			case status.State == store.StateReady:
				verify = append(verify, Action{TaskID: task.ID, Kind: ActionVerifyComplete,
					Command: "sophon verify-complete " + task.ID})
			case status.State == store.StateVerified && strings.TrimSpace(task.ValidationCommand) != "":
				if _, err := store.ReadValidation(task.MissionID, task.ID, status.Attempt); errors.Is(err, store.ErrNotFound) {
					validate = append(validate, Action{TaskID: task.ID, Kind: ActionValidate,
						Command: "sophon validate " + task.ID})
				} else if err != nil {
					return Report{}, err
				}
			}
		}
		report.Missions = append(report.Missions, entry)
	}
	report.Actions = append(verify, validate...)
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
