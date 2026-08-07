package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sophon/internal/db"
	"sophon/internal/domain"
	"sophon/internal/herdr"
)

type recordingAcquirer struct {
	store *db.Store
	lease domain.TreehouseLease
	calls int
}

func (a *recordingAcquirer) Acquire(ctx context.Context, commandID domain.CommandID, taskID domain.TaskID, attempt int) (domain.TreehouseLease, error) {
	a.calls++
	task, err := a.store.Task(ctx, taskID)
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	return a.store.RecordTreehouseLease(ctx, commandID, db.RecordTreehouseLeaseInput{
		TaskID: taskID, Attempt: attempt, ExpectedVersion: task.Version, Lease: a.lease, Actor: "test-treehouse",
	})
}

type recordingHerdr struct {
	request herdr.StartRequest
	session herdr.Session
	calls   int
	onStart func() error
}

func (h *recordingHerdr) StartCodex(_ context.Context, request herdr.StartRequest) (herdr.Session, error) {
	h.calls++
	h.request = request
	if h.onStart != nil {
		if err := h.onStart(); err != nil {
			return herdr.Session{}, err
		}
	}
	return h.session, nil
}

func (h *recordingHerdr) Observe(context.Context, herdr.Session) (herdr.State, error) {
	return herdr.StateRunning, nil
}

func (h *recordingHerdr) Wake(_ context.Context, session herdr.Session, _ string) (herdr.Session, error) {
	return session, nil
}

func TestStarterRunsMissionTaskLeaseBriefHerdrSlice(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectID, err := store.CreateProject(ctx, "cmd_project_start", db.CreateProjectInput{Name: "project", Path: "/registered/project"})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_mission_start", db.CreateMissionInput{
		ProjectID: projectID, Title: "mission title", Objective: "mission objective",
		AcceptanceCriteria: []domain.Criterion{{Description: "mission criterion"}},
		Budget:             domain.MissionBudget{MaxTaskAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "cmd_task_start", db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "task title", Objective: "task objective",
		AcceptanceCriteria: []domain.Criterion{{Description: "task criterion"}}, WorkerAgent: "codex", DeliveryMode: domain.DeliveryBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	acquirer := &recordingAcquirer{store: store, lease: domain.TreehouseLease{
		LeaseID: "lease-start", LeaseHolder: "sophon:" + string(task.ID) + ":1",
		WorktreePath: "/worktrees/start", Project: "project", Branch: "task/start",
		BaseSHA: "0123456789abcdef0123456789abcdef01234567",
	}}
	herdrAdapter := &recordingHerdr{session: herdr.Session{
		AgentName: "codex-task", AgentSessionID: "codex-session-start", SessionName: "fm-lab-start",
		WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
	}}
	starter := Starter{Store: store, Treehouse: acquirer, Herdr: herdrAdapter,
		Briefs: BriefGenerator{BaseDir: filepath.Join(root, "tasks")}, Validation: []string{"go test ./..."}}
	result, err := starter.Start(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.State != domain.TaskRunning || acquirer.calls != 1 || herdrAdapter.calls != 1 {
		t.Fatalf("start result = %+v, acquire calls=%d Herdr calls=%d", result, acquirer.calls, herdrAdapter.calls)
	}
	if result.WorkerSession.HerdrPaneID != "w1:p1" || result.WorkerSession.HerdrTabID != "w1:t1" ||
		result.WorkerSession.HerdrAgentName != "codex-task" || result.WorkerSession.AgentSessionID != "codex-session-start" {
		t.Fatalf("recorded worker session = %+v", result.WorkerSession)
	}
	if herdrAdapter.request.WorktreePath != "/worktrees/start" || herdrAdapter.request.TaskTitle != "task title" || !strings.Contains(herdrAdapter.request.Brief, "task criterion") {
		t.Fatalf("Herdr launch request = %+v", herdrAdapter.request)
	}
	if _, err := os.Stat(result.BriefPath); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.WorkerSession(ctx, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ID != result.WorkerSession.ID || persisted.HerdrPaneID != "w1:p1" {
		t.Fatalf("persisted worker session = %+v", persisted)
	}
}

func TestLaunchRecoveryErrorExplainsRetry(t *testing.T) {
	taskID := domain.TaskID("tsk_recovery_race")
	message := launchRecoveryError(taskID).Error()
	for _, want := range []string{
		"marked needs_attention by recovery",
		"sophon task retry tsk_recovery_race",
		"sophon task start tsk_recovery_race",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("launch recovery error %q omits %q", message, want)
		}
	}
}

func TestStarterExplainsPathologicalRecoveryEscalation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectID, err := store.CreateProject(ctx, "cmd_project_race", db.CreateProjectInput{Name: "project", Path: "/registered/project"})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := store.CreateMission(ctx, "cmd_mission_race", db.CreateMissionInput{
		ProjectID: projectID, Title: "mission", Objective: "objective",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "cmd_task_race", db.CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskImplementation, Title: "task", Objective: "objective",
		WorkerAgent: "codex", DeliveryMode: domain.DeliveryBranch,
	})
	if err != nil {
		t.Fatal(err)
	}
	acquirer := &recordingAcquirer{store: store, lease: domain.TreehouseLease{
		LeaseID: "lease-race", LeaseHolder: "sophon:" + string(task.ID) + ":1",
		WorktreePath: "/worktrees/race", Project: "project", Branch: "task/race",
		BaseSHA: "0123456789abcdef0123456789abcdef01234567",
	}}
	herdrAdapter := &recordingHerdr{session: herdr.Session{
		AgentName: "codex-task", AgentSessionID: "codex-session-race", SessionName: "fm-lab-race",
		WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
	}}
	herdrAdapter.onStart = func() error {
		current, err := store.Task(ctx, task.ID)
		if err != nil {
			return err
		}
		_, err = store.TransitionTask(ctx, "cmd_recovery_race", db.TransitionTaskInput{
			TaskID: current.ID, Attempt: current.CurrentAttempt, ExpectedState: current.State,
			ExpectedVersion: current.Version, To: domain.TaskNeedsAttention, Actor: "recovery",
		})
		return err
	}
	starter := Starter{Store: store, Treehouse: acquirer, Herdr: herdrAdapter,
		Briefs: BriefGenerator{BaseDir: filepath.Join(root, "tasks")}}

	_, err = starter.Start(ctx, task.ID)
	if err == nil || !strings.Contains(err.Error(), "marked needs_attention by recovery") ||
		!strings.Contains(err.Error(), "sophon task retry "+string(task.ID)) {
		t.Fatalf("pathological recovery error = %v", err)
	}
}
