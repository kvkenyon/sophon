package recovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
	"parallel-intellect/internal/worker"
)

type recoveryLabRunner struct {
	helper  string
	session string
}

func (r recoveryLabRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if len(args) < 2 || args[len(args)-2] != "--session" || args[len(args)-1] != r.session {
		return nil, nil, errors.New("recovery adapter omitted explicit lab session")
	}
	command := exec.CommandContext(ctx, r.helper, append([]string{"run", r.session}, args[:len(args)-2]...)...)
	stdout, err := command.Output()
	var stderr []byte
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr = exitErr.Stderr
	}
	return stdout, stderr, err
}

// TestRealHerdrRestartReconciliation is deliberately opt-in. Its caller must
// provision the named non-default lab and own a teardown trap before setting
// HERDR_LAB_PROVISIONED=1; every non-lifecycle call is routed through helper
// run, including calls made by the production adapter.
func TestRealHerdrRestartReconciliation(t *testing.T) {
	if os.Getenv("PARALLEL_INTELLECT_RECOVERY_SMOKE") != "1" {
		t.Skip("set PARALLEL_INTELLECT_RECOVERY_SMOKE=1 inside the guarded crash suite")
	}
	helper := strings.TrimSpace(os.Getenv("HERDR_LAB_HELPER"))
	sessionName := strings.TrimSpace(os.Getenv("HERDR_LAB_SESSION"))
	if helper == "" || sessionName == "" || os.Getenv("HERDR_LAB_PROVISIONED") != "1" {
		t.Fatal("live recovery proof requires a pre-provisioned HERDR_LAB_HELPER/HERDR_LAB_SESSION")
	}
	if !strings.HasPrefix(sessionName, "fm-lab-") || sessionName == "default" {
		t.Fatalf("unsafe Herdr lab session %q", sessionName)
	}

	worktree, err := os.MkdirTemp(".", ".herdr-m11-worker-")
	if err != nil {
		t.Fatal(err)
	}
	worktree, err = filepath.Abs(worktree)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(worktree) })
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "cmd_m11_live_project",
		db.CreateProjectInput{Name: "m11-live", Path: worktree})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(context.Background(), "cmd_m11_live_mission",
		db.CreateMissionInput{ProjectID: project, Title: "M11 live recovery", Objective: "prove Herdr restart recovery"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(context.Background(), "cmd_m11_live_task", db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "Live worker", Objective: "wait for recovery",
		WorkerAgent: "codex", DeliveryMode: domain.DeliveryBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.TransitionTask(context.Background(), "cmd_m11_live_provision", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
		To: domain.TaskProvisioning, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	branch := "pintellect/m11-live/attempt-1"
	_, err = store.RecordTreehouseLease(context.Background(), "cmd_m11_live_lease", db.RecordTreehouseLeaseInput{
		TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, Actor: "test",
		Lease: domain.TreehouseLease{LeaseID: "lease-m11-live", LeaseHolder: "holder-m11-live",
			WorktreePath: worktree, Project: "m11-live", Branch: branch,
			BaseSHA: recoveryBaseSHA, AcquiredAt: time.Now().UTC()},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.TransitionTask(context.Background(), "cmd_m11_live_starting", db.TransitionTaskInput{
		TaskID: task.ID, Attempt: 1, ExpectedState: task.State, ExpectedVersion: task.Version,
		To: domain.TaskStarting, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := recoveryLabRunner{helper: helper, session: sessionName}
	terminal := herdr.NewCommandAdapterWithRunner(sessionName, "pi-m11-recovery", runner)
	runtime, err := terminal.StartCodex(context.Background(), herdr.StartRequest{TaskID: task.ID, Attempt: 1,
		WorktreePath: worktree, Brief: "Reply exactly M11_WORKER_STARTED and then wait."})
	if err != nil {
		t.Fatal(err)
	}
	workerSession, err := store.RecordWorkerSession(context.Background(), "cmd_m11_live_worker", db.RecordWorkerSessionInput{
		TaskID: task.ID, Attempt: 1, Actor: "test",
		Session: domain.WorkerSession{ID: "wsn_m11_live", Runtime: "codex",
			HerdrSessionName: runtime.SessionName, HerdrWorkspaceID: runtime.WorkspaceID,
			HerdrTabID: runtime.TabID, HerdrPaneID: runtime.PaneID, HerdrAgentName: runtime.AgentName,
			AgentSessionID: runtime.AgentSessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRecoveryHerdrState(t, terminal, runtime, herdr.StateIdle, 4*time.Minute)
	waitRecoveryPaneText(t, runner, runtime.PaneID, "M11_WORKER_STARTED", time.Minute)

	if output, err := exec.Command(helper, "stop", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("stop isolated Herdr lab: %v: %s", err, output)
	}
	if output, err := exec.Command(helper, "provision", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("re-provision isolated Herdr lab: %v: %s", err, output)
	}
	if state, err := terminal.Observe(context.Background(), runtime); err != nil || state != herdr.StateHusk {
		t.Fatalf("restored worker state=%s err=%v, want husk", state, err)
	}

	files := worker.BriefGenerator{BaseDir: t.TempDir()}
	reconciled, err := (&worker.Reconciler{Store: store, Herdr: terminal,
		Outcomes: worker.ResultFileInspector{TaskFiles: files}, StabilizationDelay: time.Nanosecond,
		RecoveryWait: time.Minute, RecoveryPrompt: "Reply exactly M11_WORKER_RESUMED and then wait.",
		Now: func() time.Time { return time.Now().Add(time.Minute) },
	}).Reconcile(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != worker.RecoveryPromptSent || reconciled.WorkerSession.State != domain.WorkerSessionRunning ||
		reconciled.WorkerSession.ID != workerSession.ID || reconciled.WorkerSession.HerdrPaneID == workerSession.HerdrPaneID {
		t.Fatalf("reconciled worker = %+v", reconciled)
	}
	resumedRuntime := runtime
	resumedRuntime.TabID = reconciled.WorkerSession.HerdrTabID
	resumedRuntime.PaneID = reconciled.WorkerSession.HerdrPaneID
	waitRecoveryHerdrState(t, terminal, resumedRuntime, herdr.StateIdle, 4*time.Minute)
	waitRecoveryPaneText(t, runner, resumedRuntime.PaneID, "M11_WORKER_RESUMED", time.Minute)
	current, err := store.Task(context.Background(), task.ID)
	if err != nil || current.State != domain.TaskRunning {
		t.Fatalf("worker restart changed task lifecycle: task=%+v err=%v", current, err)
	}
	t.Logf("reconciled persisted Codex worker %s through Herdr lab restart %s", workerSession.ID, sessionName)
}

func waitRecoveryHerdrState(t *testing.T, terminal *herdr.CommandAdapter, session herdr.Session,
	want herdr.State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		state, err := terminal.Observe(context.Background(), session)
		if err == nil && state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Herdr worker did not reach %s: state=%s err=%v", want, state, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitRecoveryPaneText(t *testing.T, runner recoveryLabRunner, paneID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		stdout, stderr, err := runner.Run(context.Background(), "pane", "read", paneID,
			"--source", "recent", "--lines", "200", "--session", runner.session)
		if err == nil && strings.Contains(string(stdout), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane omitted %q: err=%v stderr=%s pane=%s", want, err, stderr, stdout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
