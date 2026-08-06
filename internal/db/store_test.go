package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"parallel-intellect/internal/domain"
)

func TestCASContentionAllowsExactlyOneTransition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	storeA := openTestStore(t, path)
	defer storeA.Close()
	task := createTestTask(t, storeA, domain.TaskImplementation, domain.DeliveryGate, 3)
	storeB := openTestStore(t, path)
	defer storeB.Close()

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{storeA, storeB} {
		wg.Add(1)
		go func(index int, store *Store) {
			defer wg.Done()
			<-start
			_, err := store.TransitionTask(ctx, domain.CommandID("cmd_contend_"+string(rune('a'+index))), TransitionTaskInput{
				TaskID: task.ID, Attempt: 1, ExpectedState: domain.TaskQueued,
				ExpectedVersion: 1, To: domain.TaskProvisioning, Actor: "scheduler",
			})
			results <- err
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			conflicts++
			if conflict.Current.State != domain.TaskProvisioning || conflict.Current.Version != 2 {
				t.Fatalf("conflict reloaded %+v", conflict.Current)
			}
			continue
		}
		t.Fatalf("unexpected transition error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1 each", successes, conflicts)
	}
	events, err := storeA.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != "task.provisioning" {
		t.Fatalf("events = %+v", events)
	}
}

func TestCASRejectsStaleVersionAndEmitsNoEvent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	task := createTestTask(t, store, domain.TaskImplementation, domain.DeliveryGate, 3)
	current, err := store.TransitionTask(ctx, "cmd_first", TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: domain.TaskQueued,
		ExpectedVersion: task.Version, To: domain.TaskProvisioning, Actor: "scheduler",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.TransitionTask(ctx, "cmd_stale_version", TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: domain.TaskProvisioning,
		ExpectedVersion: task.Version, To: domain.TaskStarting, Actor: "scheduler",
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale transition error = %v, want ConflictError", err)
	}
	if conflict.Current.State != current.State || conflict.Current.Version != current.Version {
		t.Fatalf("reloaded task = %+v, want %+v", conflict.Current, current)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("stale transition emitted an event: %+v", events)
	}
}

func TestCommandIdempotencyReturnsOriginalResult(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	projectID, missionID := createTestMission(t, store, 3)
	in := CreateTaskInput{
		MissionID: missionID, Kind: domain.TaskImplementation, Title: "task",
		Objective: "objective", DeliveryMode: domain.DeliveryGate,
	}
	first, err := store.CreateTask(ctx, "cmd_same", in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateTask(ctx, "cmd_same", in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.CreatedAt != second.CreatedAt {
		t.Fatalf("duplicate result changed: first=%+v second=%+v", first, second)
	}
	events, err := store.TaskEvents(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("duplicate emitted %d events, want 1", len(events))
	}
	in.Title = "different"
	if _, err := store.CreateTask(ctx, "cmd_same", in); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("reused command error = %v, want ErrCommandConflict", err)
	}
	_ = projectID
}

func TestRetryCreatesNewAttemptAndFencesOldAttempt(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	task := createTestTask(t, store, domain.TaskImplementation, domain.DeliveryGate, 2)
	failed, err := store.TransitionTask(ctx, "cmd_fail", TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: domain.TaskQueued,
		ExpectedVersion: 1, To: domain.TaskFailed, Actor: "scheduler",
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryTask(ctx, "cmd_retry", RetryTaskInput{
		TaskID: task.ID, ExpectedVersion: failed.Version, BaseSHA: "abc", Branch: "retry-2", Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != task.ID || retried.CurrentAttempt != 2 || retried.State != domain.TaskQueued || retried.Version != 3 {
		t.Fatalf("retried task = %+v", retried)
	}
	duplicate, err := store.RetryTask(ctx, "cmd_retry", RetryTaskInput{
		TaskID: task.ID, ExpectedVersion: failed.Version, BaseSHA: "abc", Branch: "retry-2", Actor: "commander",
	})
	if err != nil || duplicate.CurrentAttempt != 2 {
		t.Fatalf("duplicate retry = %+v, %v", duplicate, err)
	}
	firstAttempt, err := store.Attempt(ctx, task.ID, 1)
	if err != nil {
		t.Fatalf("attempt 1 disappeared: %v", err)
	} else if firstAttempt.CompletedAt == nil {
		t.Fatal("attempt 1 was not closed")
	}
	if attempt, err := store.Attempt(ctx, task.ID, 2); err != nil || attempt.BaseSHA != "abc" {
		t.Fatalf("attempt 2 = %+v, %v", attempt, err)
	}
	_, err = store.TransitionTask(ctx, "cmd_stale_attempt", TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: domain.TaskQueued,
		ExpectedVersion: retried.Version, To: domain.TaskProvisioning, Actor: "old-worker",
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Current.CurrentAttempt != 2 {
		t.Fatalf("old attempt error = %v", err)
	}

	current, err := store.TransitionTask(ctx, "cmd_fail_2", TransitionTaskInput{
		TaskID: task.ID, Attempt: 2, ExpectedState: domain.TaskQueued,
		ExpectedVersion: retried.Version, To: domain.TaskFailed, Actor: "scheduler",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryTask(ctx, "cmd_over_budget", RetryTaskInput{
		TaskID: task.ID, ExpectedVersion: current.Version, Actor: "commander",
	}); !errors.Is(err, ErrAttemptBudget) {
		t.Fatalf("over-budget retry error = %v", err)
	}
}

func TestRetryFromEveryWedgeFencesOldCompletion(t *testing.T) {
	states := []domain.TaskState{domain.TaskProvisioning, domain.TaskStarting, domain.TaskRunning, domain.TaskBlocked}
	for _, target := range states {
		t.Run(string(target), func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
			defer store.Close()
			current := createTestTask(t, store, domain.TaskImplementation, domain.DeliveryGate, 3)
			for _, state := range []domain.TaskState{domain.TaskProvisioning, domain.TaskStarting, domain.TaskRunning, domain.TaskBlocked} {
				var err error
				current, err = store.TransitionTask(ctx, domain.CommandID("cmd_wedge_"+string(target)+"_"+string(state)), TransitionTaskInput{TaskID: current.ID, Attempt: 1, ExpectedState: current.State, ExpectedVersion: current.Version, To: state, Actor: "test"})
				if err != nil {
					t.Fatal(err)
				}
				if state == target {
					break
				}
			}
			retried, err := store.RetryTask(ctx, domain.CommandID("cmd_retry_"+string(target)), RetryTaskInput{TaskID: current.ID, ExpectedVersion: current.Version, Actor: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			if retried.State != domain.TaskQueued || retried.CurrentAttempt != 2 {
				t.Fatalf("retry=%+v", retried)
			}
			if _, err := store.CompleteWorkerTask(ctx, domain.CommandID("cmd_stale_complete_"+string(target)), CompleteWorkerTaskInput{TaskID: current.ID, Attempt: 1, ExpectedVersion: current.Version, LeaseID: "old", LeaseHolder: "old", HeadSHA: "head", ResultPath: "result", ResultSHA256: "sum", Actor: "worker"}); !errors.Is(err, ErrStaleAttempt) {
				t.Fatalf("stale completion error=%v", err)
			}
			events, err := store.TaskEvents(ctx, current.ID)
			if err != nil {
				t.Fatal(err)
			}
			if events[len(events)-2].Type != "attempt.fenced" || events[len(events)-1].Type != "task.retry" {
				t.Fatalf("retry events=%+v", events[len(events)-2:])
			}
			started, err := store.TransitionTask(ctx, domain.CommandID("cmd_restart_"+string(target)), TransitionTaskInput{TaskID: current.ID, Attempt: 2, ExpectedState: domain.TaskQueued, ExpectedVersion: retried.Version, To: domain.TaskProvisioning, Actor: "scheduler"})
			if err != nil || started.State != domain.TaskProvisioning {
				t.Fatalf("restart=%+v err=%v", started, err)
			}
		})
	}
}

func TestEventsAreEmittedAndAppendOnly(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	task := createTestTask(t, store, domain.TaskScout, domain.DeliveryGate, 3)
	states := []domain.TaskState{domain.TaskProvisioning, domain.TaskStarting, domain.TaskRunning, domain.TaskCollecting, domain.TaskReportReady}
	current := task
	for index, state := range states {
		var err error
		current, err = store.TransitionTask(ctx, domain.CommandID("cmd_step_"+string(rune('a'+index))), TransitionTaskInput{
			TaskID: task.ID, Attempt: 1, ExpectedState: current.State,
			ExpectedVersion: current.Version, To: state, Actor: "test",
		})
		if err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1+len(states) {
		t.Fatalf("got %d events, want %d", len(events), 1+len(states))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Sequence <= events[i-1].Sequence {
			t.Fatalf("non-increasing event sequence: %+v", events)
		}
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE events SET actor = 'tampered' WHERE sequence = ?", events[0].Sequence); err == nil {
		t.Fatal("event update unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM events WHERE sequence = ?", events[0].Sequence); err == nil {
		t.Fatal("event delete unexpectedly succeeded")
	}
}

func TestMigrationUsesCommanderTerminology(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	var tableCount, columnCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'commander_sessions'").Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('missions') WHERE name = 'commander_session_id'").Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 || columnCount != 1 {
		t.Fatalf("commander schema missing: tables=%d columns=%d", tableCount, columnCount)
	}
	var legacyCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%captain%'").Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 0 {
		t.Fatalf("found %d legacy schema objects", legacyCount)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func createTestMission(t *testing.T, store *Store, maxAttempts int) (domain.ProjectID, domain.MissionID) {
	t.Helper()
	ctx := context.Background()
	suffix := t.Name()
	projectID, err := store.CreateProject(ctx, domain.CommandID("cmd_project_"+suffix), CreateProjectInput{
		Name: "project-" + suffix, Path: "/tmp/project-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, domain.CommandID("cmd_mission_"+suffix), CreateMissionInput{
		ProjectID: projectID, Title: "mission", Objective: "objective",
		Budget: domain.MissionBudget{MaxTaskAttempts: maxAttempts},
	})
	if err != nil {
		t.Fatal(err)
	}
	return projectID, mission.ID
}

func createTestTask(t *testing.T, store *Store, kind domain.TaskKind, mode domain.DeliveryMode, maxAttempts int) domain.Task {
	t.Helper()
	_, missionID := createTestMission(t, store, maxAttempts)
	task, err := store.CreateTask(context.Background(), domain.CommandID("cmd_task_"+t.Name()), CreateTaskInput{
		MissionID: missionID, Kind: kind, Title: "task", Objective: "objective", DeliveryMode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
