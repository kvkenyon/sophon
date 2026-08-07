package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"sophon/internal/domain"
	"sophon/internal/signals"
)

func TestSignalLifecyclePersistsAnswerAndEmitsEvents(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	task := createTestTask(t, store, domain.TaskImplementation, domain.DeliveryGate, 3)

	created, err := store.CreateSignal(ctx, "cmd_signal_create", CreateSignalInput{
		MissionID: task.MissionID, TaskID: &task.ID, Kind: signals.SignalDecision,
		Question: "Which compatibility contract?", Context: "Two clients differ.",
		Options:        []signals.Option{{Value: "legacy"}, {Value: "strict", Description: "Reject legacy input."}},
		Recommendation: "strict", Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != signals.SignalOpen || created.Answer != nil || created.Version != 1 {
		t.Fatalf("created signal = %+v", created)
	}

	resolveInput := ResolveSignalInput{
		SignalID: created.ID, ExpectedVersion: created.Version,
		Answer: "Use strict compatibility.", Actor: "operator",
	}
	resolved, err := store.ResolveSignal(ctx, "cmd_signal_resolve", resolveInput)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != signals.SignalResolved || resolved.Answer == nil ||
		*resolved.Answer != "Use strict compatibility." || resolved.Version != 2 || resolved.ResolvedAt == nil {
		t.Fatalf("resolved signal = %+v", resolved)
	}
	persisted, err := store.Signal(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Answer == nil || *persisted.Answer != *resolved.Answer || len(persisted.Options) != 2 {
		t.Fatalf("persisted signal = %+v", persisted)
	}
	duplicate, err := store.ResolveSignal(ctx, "cmd_signal_resolve", resolveInput)
	if err != nil || duplicate.Version != resolved.Version || duplicate.Answer == nil || *duplicate.Answer != *resolved.Answer {
		t.Fatalf("duplicate resolution = %+v, %v", duplicate, err)
	}

	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEventTypes(events, "signal.created", "signal.resolved") {
		t.Fatalf("signal events missing: %+v", events)
	}
	var resolvedEvents int
	for _, event := range events {
		if event.Type == "signal.resolved" {
			resolvedEvents++
		}
	}
	if resolvedEvents != 1 {
		t.Fatalf("signal.resolved events = %d, want 1", resolvedEvents)
	}
}

func TestCreateSignalIsCommandIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	task := createTestTask(t, store, domain.TaskReview, domain.DeliveryGate, 3)
	in := CreateSignalInput{
		MissionID: task.MissionID, TaskID: &task.ID, Kind: signals.SignalDecision,
		Question: "Ship this behavior?", Actor: "reviewer",
	}
	first, err := store.CreateSignal(ctx, "cmd_signal_same", in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateSignal(ctx, "cmd_signal_same", in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("idempotent result changed: first=%+v second=%+v", first, second)
	}
	in.Question = "A different decision?"
	if _, err := store.CreateSignal(ctx, "cmd_signal_same", in); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("command reuse error = %v, want ErrCommandConflict", err)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var createdEvents int
	for _, event := range events {
		if event.Type == "signal.created" {
			createdEvents++
		}
	}
	if createdEvents != 1 {
		t.Fatalf("signal.created events = %d, want 1", createdEvents)
	}
}

func TestOpenSignalDependencyGatesProvisioningUntilResolution(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	task := createTestTask(t, store, domain.TaskImplementation, domain.DeliveryGate, 3)
	signal, err := store.CreateSignal(ctx, "cmd_gate_signal", CreateSignalInput{
		MissionID: task.MissionID, Kind: signals.SignalDecision,
		Question: "Which storage format?", Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTaskSignalDependency(ctx, "cmd_gate_dependency", AddTaskSignalDependencyInput{
		TaskID: task.ID, SignalID: signal.ID, Actor: "commander",
	}); err != nil {
		t.Fatal(err)
	}

	_, err = store.TransitionTask(ctx, "cmd_gate_blocked", TransitionTaskInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State,
		ExpectedVersion: task.Version, To: domain.TaskProvisioning, Actor: "scheduler",
	})
	var dependencyErr *OpenSignalDependenciesError
	if !errors.As(err, &dependencyErr) || !errors.Is(err, ErrOpenSignalDependencies) {
		t.Fatalf("blocked transition error = %v", err)
	}
	if len(dependencyErr.SignalIDs) != 1 || dependencyErr.SignalIDs[0] != signal.ID {
		t.Fatalf("open dependencies = %v", dependencyErr.SignalIDs)
	}
	current, err := store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.TaskQueued || current.Version != task.Version {
		t.Fatalf("blocked transition mutated task: %+v", current)
	}

	if _, err := store.ResolveSignal(ctx, "cmd_gate_resolve", ResolveSignalInput{
		SignalID: signal.ID, ExpectedVersion: signal.Version, Answer: "Use JSON.", Actor: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	provisioning, err := store.TransitionTask(ctx, "cmd_gate_released", TransitionTaskInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State,
		ExpectedVersion: task.Version, To: domain.TaskProvisioning, Actor: "scheduler",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provisioning.State != domain.TaskProvisioning {
		t.Fatalf("task state = %s", provisioning.State)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEventTypes(events, "task.signal_dependency_added", "task.provisioning") {
		t.Fatalf("dependency events missing: %+v", events)
	}
	for _, event := range events {
		if event.CommandID != nil && *event.CommandID == "cmd_gate_blocked" {
			t.Fatalf("blocked transition emitted event: %+v", event)
		}
	}
}

func TestSignalResolutionUsesCAS(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	defer store.Close()
	_, missionID := createTestMission(t, store, 3)
	signal, err := store.CreateSignal(ctx, "cmd_cas_signal", CreateSignalInput{
		MissionID: missionID, Kind: signals.SignalDecision, Question: "Choose?", Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSignal(ctx, "cmd_cas_stale", ResolveSignalInput{
		SignalID: signal.ID, ExpectedVersion: signal.Version + 1, Answer: "A", Actor: "operator",
	}); err == nil {
		t.Fatal("stale signal resolution unexpectedly succeeded")
	} else {
		var conflict *SignalConflictError
		if !errors.As(err, &conflict) || conflict.Current.Status != signals.SignalOpen {
			t.Fatalf("stale resolution error = %v", err)
		}
	}
}

func hasEventTypes(events []domain.Event, wanted ...string) bool {
	seen := make(map[string]bool)
	for _, event := range events {
		seen[event.Type] = true
	}
	for _, eventType := range wanted {
		if !seen[eventType] {
			return false
		}
	}
	return true
}
