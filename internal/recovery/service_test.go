package recovery

import (
	"context"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	taskpolicy "parallel-intellect/internal/task"
	"parallel-intellect/internal/treehouse"
	"parallel-intellect/internal/validation"
	"parallel-intellect/internal/worker"
)

const (
	recoveryBaseSHA = "1111111111111111111111111111111111111111"
	recoveryHeadSHA = "2222222222222222222222222222222222222222"
)

type fixedLeases struct{ result treehouse.ReconcileResult }

func (f fixedLeases) Reconcile(context.Context) (treehouse.ReconcileResult, error) {
	return f.result, nil
}

type fixedWorker struct {
	result worker.RecoveryResult
	calls  *int
}

func (f fixedWorker) Reconcile(context.Context, domain.TaskID) (worker.RecoveryResult, error) {
	(*f.calls)++
	return f.result, nil
}

type fixedCompletion struct {
	task  domain.Task
	calls int
}

func (f *fixedCompletion) Resume(context.Context, domain.TaskID) (domain.Task, error) {
	f.calls++
	return f.task, nil
}

func TestAbandonedStartingTaskEscalatesAfterStabilizationWindow(t *testing.T) {
	store, task := recoveryTask(t, domain.TaskStarting, false)
	defer store.Close()
	workerFactoryCalls := 0
	service := Service{Store: store, Leases: fixedLeases{}, Now: func() time.Time {
		return task.UpdatedAt.Add(taskpolicy.InFlightStabilizationWindow)
	}, Worker: func(domain.WorkerSession) WorkerReconciler {
		workerFactoryCalls++
		return nil
	}}
	report, err := service.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tasks) != 1 || report.Tasks[0].Status != StatusWorkerMissing ||
		report.Tasks[0].State != domain.TaskNeedsAttention || report.Tasks[0].Outcome != OutcomeRecoverable {
		t.Fatalf("report = %+v", report)
	}
	if workerFactoryCalls != 0 {
		t.Fatalf("missing worker caused %d replacement attempts", workerFactoryCalls)
	}
	current, err := store.Task(context.Background(), task.ID)
	if err != nil || current.State != domain.TaskNeedsAttention {
		t.Fatalf("current task=%+v err=%v", current, err)
	}
	lease, err := store.TreehouseLease(context.Background(), task.ID, 1)
	if err != nil || lease.State != domain.TreehouseLeaseActive {
		t.Fatalf("missing worker altered lease=%+v err=%v", lease, err)
	}
}

func TestStartupRecoveryPassDuringWorkerLaunchLeavesSessionRecordable(t *testing.T) {
	store, task := recoveryTask(t, domain.TaskStarting, false)
	defer store.Close()
	service := Service{Store: store, Leases: fixedLeases{}}

	report, err := service.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tasks) != 1 || report.Tasks[0].State != domain.TaskStarting ||
		report.Tasks[0].Status != StatusAwaitingWorkerStart {
		t.Fatalf("recovery interrupted in-flight launch: %+v", report)
	}

	_, err = store.RecordWorkerSession(context.Background(), "cmd_recovery_race_worker", db.RecordWorkerSessionInput{
		TaskID: task.ID, Attempt: 1, Actor: "scheduler",
		Session: domain.WorkerSession{ID: "wsn_recovery_race", Runtime: "codex",
			HerdrSessionName: "fm-lab", HerdrWorkspaceID: "w1", HerdrTabID: "t1", HerdrPaneID: "p1",
			HerdrAgentName: "pi-task-a1", AgentSessionID: "codex-session"},
	})
	if err != nil {
		t.Fatalf("record worker session after recovery pass: %v", err)
	}
	current, err := store.Task(context.Background(), task.ID)
	if err != nil || current.State != domain.TaskRunning {
		t.Fatalf("task after session record = %+v, error = %v", current, err)
	}
}

func TestStartupObservesExistingWorkerSession(t *testing.T) {
	store, task := recoveryTask(t, domain.TaskRunning, true)
	defer store.Close()
	calls := 0
	service := Service{Store: store, Leases: fixedLeases{result: treehouse.ReconcileResult{Valid: 1}},
		Worker: func(domain.WorkerSession) WorkerReconciler {
			return fixedWorker{calls: &calls, result: worker.RecoveryResult{
				Status: worker.RecoveryRunning, Task: task,
			}}
		}}
	report, err := service.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(report.Tasks) != 1 || report.Tasks[0].Status != StatusWorkerObserved ||
		report.Tasks[0].Outcome != OutcomeExactlyOnce {
		t.Fatalf("calls=%d report=%+v", calls, report)
	}
}

func TestStartupResumesStructuredCompletion(t *testing.T) {
	store, task := recoveryTask(t, domain.TaskRunning, true)
	defer store.Close()
	ready := task
	ready.State = domain.TaskReady
	completion := &fixedCompletion{task: ready}
	calls := 0
	service := Service{Store: store, Leases: fixedLeases{}, Completion: completion,
		Worker: func(domain.WorkerSession) WorkerReconciler {
			return fixedWorker{calls: &calls, result: worker.RecoveryResult{
				Status: worker.RecoveryStructured, Outcome: worker.OutcomeCompletion, Task: task,
			}}
		}}
	report, err := service.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completion.calls != 1 || report.Tasks[0].Status != StatusCompletionResumed ||
		report.Tasks[0].State != domain.TaskReady || report.Tasks[0].Outcome != OutcomeExactlyOnce {
		t.Fatalf("completion calls=%d report=%+v", completion.calls, report)
	}
}

func TestStartupMarksPartialValidationExplicitlyResumable(t *testing.T) {
	store, task := recoveryTask(t, domain.TaskRunning, true)
	defer store.Close()
	current, err := store.CompleteWorkerTask(context.Background(), "cmd_recovery_complete", db.CompleteWorkerTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, LeaseID: "lease-recovery",
		LeaseHolder: "holder-recovery", HeadSHA: recoveryHeadSHA, ResultPath: "/result.json",
		ResultSHA256: "result-hash", Actor: "worker", Result: domain.WorkerResult{Version: 1,
			Status: "completed", Summary: "done", Verification: []domain.VerificationResult{{Command: "test"}},
			ChangedFiles: []string{"file.go"}, Risks: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = store.BeginValidation(context.Background(), "cmd_recovery_validation", validationBegin(current))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := Service{Store: store, Leases: fixedLeases{}, Worker: func(domain.WorkerSession) WorkerReconciler {
		return fixedWorker{calls: &calls, result: worker.RecoveryResult{Status: worker.RecoveryIdle, Task: current}}
	}}
	report, err := service.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Tasks[0].Status != StatusValidationResumable || report.Tasks[0].Outcome != OutcomeRecoverable ||
		report.Tasks[0].State != domain.TaskValidating {
		t.Fatalf("report = %+v", report)
	}
}

func validationBegin(task domain.Task) validation.BeginInput {
	return validation.BeginInput{TaskID: task.ID, Attempt: task.CurrentAttempt, Actor: "test"}
}

func recoveryTask(t *testing.T, state domain.TaskState, withWorker bool) (*db.Store, domain.Task) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, t.TempDir()+"/state.db")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "cmd_recovery_project", db.CreateProjectInput{Name: "project", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_recovery_mission", db.CreateMissionInput{
		ProjectID: project, Title: "Recovery", Objective: "recover",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "cmd_recovery_task", db.CreateTaskInput{MissionID: mission.ID,
		Kind: domain.TaskImplementation, Title: "Task", Objective: "recover task", WorkerAgent: "codex",
		DeliveryMode: domain.DeliveryPR})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.TransitionTask(ctx, "cmd_recovery_provision", db.TransitionTaskInput{TaskID: task.ID,
		Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version, To: domain.TaskProvisioning, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordTreehouseLease(ctx, "cmd_recovery_lease", db.RecordTreehouseLeaseInput{TaskID: task.ID,
		Attempt: 1, ExpectedVersion: task.Version, Actor: "test", Lease: domain.TreehouseLease{
			LeaseID: "lease-recovery", LeaseHolder: "holder-recovery", WorktreePath: t.TempDir(),
			Project: "project", Branch: "pintellect/recovery/attempt-1", BaseSHA: recoveryBaseSHA,
			AcquiredAt: time.Unix(1, 0).UTC(),
		}})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state == domain.TaskProvisioning {
		return store, task
	}
	task, err = store.TransitionTask(ctx, "cmd_recovery_starting", db.TransitionTaskInput{TaskID: task.ID,
		Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version, To: domain.TaskStarting, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if state == domain.TaskStarting {
		return store, task
	}
	if !withWorker {
		t.Fatal("running fixture requires a worker")
	}
	_, err = store.RecordWorkerSession(ctx, "cmd_recovery_worker", db.RecordWorkerSessionInput{TaskID: task.ID,
		Attempt: 1, Actor: "test", Session: domain.WorkerSession{
			ID: "wsn_recovery", Runtime: "codex", HerdrSessionName: "fm-lab", HerdrWorkspaceID: "w1",
			HerdrTabID: "t1", HerdrPaneID: "p1", HerdrAgentName: "pi-task-a1", AgentSessionID: "codex-session",
		}})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, task
}
