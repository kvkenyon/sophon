package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/db"
	"sophon/internal/domain"
	"sophon/internal/herdr"
	taskpolicy "sophon/internal/task"
)

type recoveryHerdr struct {
	observations  []herdr.State
	wakeSessions  []herdr.Session
	wakeMessages  []string
	startCalls    int
	terminalProse string
	wakeErr       error
	wakeResult    *herdr.Session
}

func (h *recoveryHerdr) StartCodex(context.Context, herdr.StartRequest) (herdr.Session, error) {
	h.startCalls++
	return herdr.Session{}, fmt.Errorf("unexpected fresh worker start")
}

func (h *recoveryHerdr) Observe(context.Context, herdr.Session) (herdr.State, error) {
	if len(h.observations) == 0 {
		return herdr.StateIdle, nil
	}
	state := h.observations[0]
	h.observations = h.observations[1:]
	return state, nil
}

func (h *recoveryHerdr) Wake(_ context.Context, session herdr.Session, message string) (herdr.Session, error) {
	h.wakeSessions = append(h.wakeSessions, session)
	h.wakeMessages = append(h.wakeMessages, message)
	if h.wakeResult != nil {
		return *h.wakeResult, h.wakeErr
	}
	return session, h.wakeErr
}

type fixedOutcome struct {
	kind OutcomeKind
}

func TestForgottenCompletionUsesInFlightStabilizationWindow(t *testing.T) {
	reconciler := Reconciler{}
	if got := reconciler.stabilizationDelay(); got != taskpolicy.InFlightStabilizationWindow {
		t.Fatalf("default completion stabilization = %s, want %s", got, taskpolicy.InFlightStabilizationWindow)
	}
}

func (o fixedOutcome) Inspect(context.Context, domain.Task, domain.TaskAttempt) (OutcomeKind, error) {
	return o.kind, nil
}

func TestWakeReusesSameWorkerSessionAndLeavesTaskLifecycleAlone(t *testing.T) {
	for _, state := range []domain.WorkerSessionState{domain.WorkerSessionIdle, domain.WorkerSessionInactive} {
		t.Run(string(state), func(t *testing.T) {
			store, task, running := setupRunningWorker(t)
			original := transitionWorkerFixture(t, store, running, domain.WorkerSessionIdle, "idle")
			if state == domain.WorkerSessionInactive {
				original = transitionWorkerFixture(t, store, original, domain.WorkerSessionInactive, "inactive")
			}
			runtime := &recoveryHerdr{}
			waker := Waker{Store: store, Herdr: runtime}
			woken, err := waker.Wake(context.Background(), WakeRequest{
				TaskID: task.ID, CommandID: domain.CommandID("cmd_wake_same_" + string(state)), Message: "apply review feedback",
			})
			if err != nil {
				t.Fatal(err)
			}
			if woken.ID != original.ID || woken.HerdrPaneID != original.HerdrPaneID || woken.State != domain.WorkerSessionRunning {
				t.Fatalf("woken session = %+v, original = %+v", woken, original)
			}
			if len(runtime.wakeSessions) != 1 || runtime.wakeSessions[0].PaneID != original.HerdrPaneID || runtime.startCalls != 0 {
				t.Fatalf("wake calls = %+v, start calls = %d", runtime.wakeSessions, runtime.startCalls)
			}
			unchanged, err := store.Task(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.State != domain.TaskRunning || unchanged.Version != task.Version {
				t.Fatalf("worker wake changed task lifecycle: before=%+v after=%+v", task, unchanged)
			}
		})
	}
}

func TestForgottenCompletionSendsOneRecoveryPromptThenNeedsAttention(t *testing.T) {
	store, task, _ := setupRunningWorker(t)
	runtime := &recoveryHerdr{
		observations:  []herdr.State{herdr.StateIdle, herdr.StateIdle, herdr.StateIdle, herdr.StateIdle},
		terminalProse: "done; all fixed; tests pass",
	}
	now := time.Now().UTC()
	reconciler := Reconciler{Store: store, Herdr: runtime, Outcomes: fixedOutcome{kind: OutcomeNone},
		StabilizationDelay: time.Minute, RecoveryWait: time.Minute, Now: func() time.Time { return now }}

	first, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != RecoveryStabilizing || first.WorkerSession.State != domain.WorkerSessionIdle {
		t.Fatalf("first recovery step = %+v", first)
	}
	stillRunning, err := store.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillRunning.State != domain.TaskRunning {
		t.Fatalf("idle worker completed task: %+v", stillRunning)
	}

	now = first.WorkerSession.IdleAt.Add(2 * time.Minute)
	second, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != RecoveryPromptSent || len(runtime.wakeSessions) != 1 {
		t.Fatalf("recovery prompt step = %+v, wakes=%d", second, len(runtime.wakeSessions))
	}
	if runtime.wakeSessions[0].PaneID != first.WorkerSession.HerdrPaneID || runtime.startCalls != 0 {
		t.Fatalf("recovery did not reuse same worker: wakes=%+v starts=%d", runtime.wakeSessions, runtime.startCalls)
	}

	now = second.WorkerSession.RecoveryPromptAt.Add(2 * time.Minute)
	third, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if third.Status != RecoveryNeedsAttention || third.Task.State != domain.TaskNeedsAttention {
		t.Fatalf("unresolved recovery step = %+v", third)
	}
	if len(runtime.wakeSessions) != 1 {
		t.Fatalf("sent %d recovery prompts, want exactly one", len(runtime.wakeSessions))
	}
	if third.Task.State == domain.TaskReady || third.Task.State == domain.TaskDelivered || third.Task.State == domain.TaskDeliveredBranch {
		t.Fatalf("terminal prose %q completed task: %+v", runtime.terminalProse, third.Task)
	}

	fourth, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Status != RecoveryIdle || len(runtime.wakeSessions) != 1 {
		t.Fatalf("post-escalation recovery = %+v, wakes=%d", fourth, len(runtime.wakeSessions))
	}
}

func TestHuskBecomesInactiveThenRecoveryWakesPersistedSession(t *testing.T) {
	store, task, _ := setupRunningWorker(t)
	runtime := &recoveryHerdr{observations: []herdr.State{herdr.StateHusk, herdr.StateHusk},
		wakeResult: &herdr.Session{SessionName: "fm-lab", WorkspaceID: "w1", TabID: "w1:t2", PaneID: "w1:p2",
			AgentName: "pi-task-a1", AgentSessionID: "codex-session-1"}}
	now := time.Now().UTC()
	reconciler := Reconciler{Store: store, Herdr: runtime, Outcomes: fixedOutcome{kind: OutcomeNone},
		StabilizationDelay: time.Minute, Now: func() time.Time { return now }}
	first, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != RecoveryStabilizing || first.WorkerSession.State != domain.WorkerSessionInactive || len(runtime.wakeSessions) != 0 {
		t.Fatalf("husk stabilization = %+v, wakes=%d", first, len(runtime.wakeSessions))
	}
	unchanged, err := store.Task(context.Background(), task.ID)
	if err != nil || unchanged.State != domain.TaskRunning || unchanged.Version != task.Version {
		t.Fatalf("husk changed task lifecycle: before=%+v after=%+v error=%v", task, unchanged, err)
	}
	now = first.WorkerSession.InactiveAt.Add(2 * time.Minute)
	second, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != RecoveryPromptSent || second.WorkerSession.State != domain.WorkerSessionRunning || len(runtime.wakeSessions) != 1 {
		t.Fatalf("husk recovery = %+v, wakes=%d", second, len(runtime.wakeSessions))
	}
	if second.WorkerSession.HerdrWorkspaceID != "w1" || second.WorkerSession.HerdrTabID != "w1:t2" || second.WorkerSession.HerdrPaneID != "w1:p2" {
		t.Fatalf("replacement placement was not persisted: %+v", second.WorkerSession)
	}
	if runtime.wakeSessions[0].AgentSessionID != "codex-session-1" || runtime.wakeSessions[0].PaneID != "w1:p1" {
		t.Fatalf("husk wake lost resumable identity: %+v", runtime.wakeSessions[0])
	}
}

func TestWakeMissingPaneMarksLostAndNeedsAttention(t *testing.T) {
	store, task, running := setupRunningWorker(t)
	transitionWorkerFixture(t, store, running, domain.WorkerSessionIdle, "idle")
	runtime := &recoveryHerdr{wakeErr: herdr.ErrSessionMissing}
	waker := Waker{Store: store, Herdr: runtime}
	_, err := waker.Wake(context.Background(), WakeRequest{TaskID: task.ID, CommandID: "cmd_wake_missing", Message: "continue"})
	if !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("missing wake error = %v", err)
	}
	persistedTask, err := store.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	persistedSession, err := store.WorkerSession(context.Background(), task.ID, task.CurrentAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if persistedTask.State != domain.TaskNeedsAttention || persistedSession.State != domain.WorkerSessionLost {
		t.Fatalf("missing wake reconciliation: task=%+v session=%+v", persistedTask, persistedSession)
	}
	lease, err := store.TreehouseLease(context.Background(), task.ID, task.CurrentAttempt)
	if err != nil || lease.State != domain.TreehouseLeaseActive {
		t.Fatalf("missing wake altered lease: %+v, %v", lease, err)
	}
}

func TestRecoveryRecognizesOnlyStructuredOutcomeKinds(t *testing.T) {
	for _, kind := range []OutcomeKind{OutcomeCompletion, OutcomeFailure, OutcomeBlocker} {
		t.Run(string(kind), func(t *testing.T) {
			store, task, running := setupRunningWorker(t)
			idle := transitionWorkerFixture(t, store, running, domain.WorkerSessionIdle, "idle")
			now := idle.IdleAt.Add(time.Hour)
			runtime := &recoveryHerdr{observations: []herdr.State{herdr.StateIdle}}
			reconciler := Reconciler{Store: store, Herdr: runtime, Outcomes: fixedOutcome{kind: kind},
				StabilizationDelay: time.Second, Now: func() time.Time { return now }}
			result, err := reconciler.Reconcile(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != RecoveryStructured || result.Outcome != kind || len(runtime.wakeSessions) != 0 {
				t.Fatalf("structured recovery = %+v, wakes=%d", result, len(runtime.wakeSessions))
			}
			persisted, err := store.Task(context.Background(), task.ID)
			if err != nil || persisted.State != domain.TaskRunning {
				t.Fatalf("structured observation mutated task = %+v, %v", persisted, err)
			}
		})
	}
}

func TestLostWorkerEscalatesWithoutReplacingOrReleasingLease(t *testing.T) {
	store, task, running := setupRunningWorker(t)
	runtime := &recoveryHerdr{observations: []herdr.State{herdr.StateLost}}
	reconciler := Reconciler{Store: store, Herdr: runtime, Outcomes: fixedOutcome{kind: OutcomeNone}}
	result, err := reconciler.Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RecoveryLost || result.Task.State != domain.TaskNeedsAttention ||
		result.WorkerSession.State != domain.WorkerSessionLost {
		t.Fatalf("lost reconciliation = %+v", result)
	}
	if result.WorkerSession.ID != running.ID || runtime.startCalls != 0 || len(runtime.wakeSessions) != 0 {
		t.Fatalf("lost session was replaced: result=%+v runtime=%+v", result.WorkerSession, runtime)
	}
	lease, err := store.TreehouseLease(context.Background(), task.ID, task.CurrentAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != domain.TreehouseLeaseActive {
		t.Fatalf("lost reconciliation altered active lease: %+v", lease)
	}
}

func TestResultFileInspectorRejectsTerminalProse(t *testing.T) {
	files := BriefGenerator{BaseDir: filepath.Join(t.TempDir(), "tasks")}
	task := domain.Task{ID: "tsk_prose"}
	attempt := domain.TaskAttempt{TaskID: task.ID, Attempt: 1}
	dir, err := files.AttemptDir(task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "result.json")
	if err := os.WriteFile(path, []byte("done; tests pass"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := ResultFileInspector{TaskFiles: files}
	if got, err := inspector.Inspect(context.Background(), task, attempt); err != nil || got != OutcomeNone {
		t.Fatalf("terminal prose outcome = %s, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"status":"completed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := inspector.Inspect(context.Background(), task, attempt); err != nil || got != OutcomeCompletion {
		t.Fatalf("structured outcome = %s, %v", got, err)
	}
}

func setupRunningWorker(t *testing.T) (*db.Store, domain.Task, domain.WorkerSession) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	suffix := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	project, err := store.CreateProject(ctx, domain.CommandID("cmd_project_"+suffix), db.CreateProjectInput{
		Name: "project-" + suffix, Path: "/tmp/project-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, domain.CommandID("cmd_mission_"+suffix), db.CreateMissionInput{
		ProjectID: project, Title: "mission", Objective: "objective", Budget: domain.MissionBudget{MaxTaskAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, domain.CommandID("cmd_task_"+suffix), db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "task", Objective: "objective",
		WorkerAgent: "codex", DeliveryMode: domain.DeliveryBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	task = recoveryTaskTransition(t, store, task, domain.TaskProvisioning, "provision")
	_, err = store.RecordTreehouseLease(ctx, domain.CommandID("cmd_lease_"+suffix), db.RecordTreehouseLeaseInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, Actor: "test",
		Lease: domain.TreehouseLease{LeaseID: "lease-" + suffix, LeaseHolder: "sophon:" + string(task.ID) + ":1",
			WorktreePath: "/tmp/worktree-" + suffix, Project: "project-" + suffix,
			Branch: "task/" + suffix, BaseSHA: "0123456789abcdef0123456789abcdef01234567"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = recoveryTaskTransition(t, store, task, domain.TaskStarting, "starting")
	session, err := store.RecordWorkerSession(ctx, domain.CommandID("cmd_worker_"+suffix), db.RecordWorkerSessionInput{
		TaskID: task.ID, Attempt: 1, Actor: "test",
		Session: domain.WorkerSession{ID: domain.SessionID("wsn_" + suffix), Runtime: "codex",
			HerdrSessionName: "fm-lab-recovery", HerdrWorkspaceID: "w1", HerdrTabID: "w1:t1", HerdrPaneID: "w1:p1",
			HerdrAgentName: "pi-task-a1", AgentSessionID: "codex-session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, task, session
}

func recoveryTaskTransition(t *testing.T, store *db.Store, task domain.Task, to domain.TaskState, label string) domain.Task {
	t.Helper()
	updated, err := store.TransitionTask(context.Background(), domain.CommandID("cmd_"+label+"_"+strings.ReplaceAll(t.Name(), "/", "-")), db.TransitionTaskInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State,
		ExpectedVersion: task.Version, To: to, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func transitionWorkerFixture(t *testing.T, store *db.Store, session domain.WorkerSession, to domain.WorkerSessionState, label string) domain.WorkerSession {
	t.Helper()
	updated, err := store.TransitionWorkerSession(context.Background(), domain.CommandID("cmd_worker_"+label+"_"+strings.ReplaceAll(t.Name(), "/", "-")), db.TransitionWorkerSessionInput{
		SessionID: session.ID, TaskID: session.TaskID, Attempt: session.Attempt,
		ExpectedState: session.State, ExpectedVersion: session.Version, To: to, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}
