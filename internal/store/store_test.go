package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
)

func useHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(datahome.OverrideEnv, home)
	return home
}

func sampleTask(missionID, taskID string) Task {
	return Task{ID: taskID, MissionID: missionID, Title: "Add feature", Kind: domain.TaskImplementation,
		DeliveryMode: domain.DeliveryBranch, CreatedAt: time.Now().UTC()}
}

func TestPublishRoundTripAndOverwrite(t *testing.T) {
	home := useHome(t)
	path := AttemptPath(home, "mission_1", "task_1", 1, "outcome.json")
	first := Outcome{TaskID: "task_1", Attempt: 1, HeadSHA: "aaa", Branch: "sophon/x/attempt-1",
		ResultSHA256: "111", VerifiedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := Publish(path, first); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	var got Outcome
	if err := read(path, &got); err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("got %+v, want %+v", got, first)
	}
	second := first
	second.HeadSHA = "bbb"
	if err := Publish(path, second); err != nil {
		t.Fatal(err)
	}
	if err := read(path, &got); err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "bbb" {
		t.Fatalf("overwrite did not take effect: %+v", got)
	}
	// Publication cleans up its temporary files.
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".publish-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files leaked: %v", leftovers)
	}
}

func TestPublishBytesPreservesVerbatim(t *testing.T) {
	home := useHome(t)
	path := AttemptPath(home, "mission_1", "task_1", 2, "brief.md")
	payload := []byte("# brief\n\nnot json: [{\n")
	if err := PublishBytes(path, payload); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("bytes = %q, want %q", data, payload)
	}
}

func TestReadNotFoundSemantics(t *testing.T) {
	useHome(t)
	_, err := ReadSpawn("mission_none", "task_none", 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	_, err = FindTask("task_none")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateAndListMissionsAndTasks(t *testing.T) {
	useHome(t)
	mission := Mission{ID: "mission_a", ProjectPath: "/repo", Title: "Ship", Objective: "Do it",
		CreatedAt: time.Now().UTC()}
	if err := CreateMission(mission); err != nil {
		t.Fatal(err)
	}
	task := sampleTask(mission.ID, "task_a")
	if err := CreateTask(task); err != nil {
		t.Fatal(err)
	}
	missions, err := ListMissions()
	if err != nil || len(missions) != 1 {
		t.Fatalf("missions = %v, %v", missions, err)
	}
	tasks, err := ListTasks(mission.ID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("tasks = %v, %v", tasks, err)
	}
	found, err := FindTask(task.ID)
	if err != nil || found.MissionID != mission.ID {
		t.Fatalf("FindTask = %+v, %v", found, err)
	}
	// A task under a missing mission is refused.
	if err := CreateTask(sampleTask("mission_missing", "task_b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBumpAttempt(t *testing.T) {
	useHome(t)
	mission := Mission{ID: "mission_a", CreatedAt: time.Now().UTC()}
	if err := CreateMission(mission); err != nil {
		t.Fatal(err)
	}
	task := sampleTask(mission.ID, "task_a")
	if err := CreateTask(task); err != nil {
		t.Fatal(err)
	}
	updated, err := BumpAttempt(mission.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentAttempt != 1 {
		t.Fatalf("CurrentAttempt = %d, want 1", updated.CurrentAttempt)
	}
	reloaded, err := ReadTask(mission.ID, task.ID)
	if err != nil || reloaded.CurrentAttempt != 1 {
		t.Fatalf("reloaded = %+v, %v", reloaded, err)
	}
}

func TestDeriveLifecycle(t *testing.T) {
	home := useHome(t)
	mission := Mission{ID: "mission_a", CreatedAt: time.Now().UTC()}
	if err := CreateMission(mission); err != nil {
		t.Fatal(err)
	}
	task := sampleTask(mission.ID, "task_a")
	if err := CreateTask(task); err != nil {
		t.Fatal(err)
	}
	assertState := func(want string) {
		t.Helper()
		task, err := ReadTask(mission.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		status, err := Derive(task)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != want {
			t.Fatalf("state = %q, want %q (attempt %d)", status.State, want, status.Attempt)
		}
	}
	assertState(StateQueued)
	if _, err := BumpAttempt(mission.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	assertState(StateActive)
	publishAttempt := func(attempt int, name string, v any) {
		t.Helper()
		if err := Publish(AttemptPath(home, mission.ID, task.ID, attempt, name), v); err != nil {
			t.Fatal(err)
		}
	}
	publishAttempt(1, "result.json", map[string]string{"status": "completed"})
	assertState(StateReady)
	publishAttempt(1, "outcome.json", Outcome{TaskID: task.ID, Attempt: 1})
	assertState(StateVerified)
	publishAttempt(1, "delivery.json", Delivery{TaskID: task.ID, Attempt: 1, State: DeliveryPending})
	assertState(StateVerified)
	publishAttempt(1, "delivery.json", Delivery{TaskID: task.ID, Attempt: 1, State: DeliveryDeliveredBranch})
	assertState(StateDelivered)
}

// TestDeriveFencesNonCurrentAttempts proves a result in a fenced attempt
// never changes the task's derived state.
func TestDeriveFencesNonCurrentAttempts(t *testing.T) {
	home := useHome(t)
	mission := Mission{ID: "mission_a", CreatedAt: time.Now().UTC()}
	if err := CreateMission(mission); err != nil {
		t.Fatal(err)
	}
	task := sampleTask(mission.ID, "task_a")
	if err := CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := BumpAttempt(mission.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := BumpAttempt(mission.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	// Attempt 1 is fenced and complete; attempt 2 is bare.
	if err := Publish(AttemptPath(home, mission.ID, task.ID, 1, "result.json"), map[string]string{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := Publish(AttemptPath(home, mission.ID, task.ID, 1, "outcome.json"), Outcome{TaskID: task.ID, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	current, err := ReadTask(mission.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := Derive(current)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateActive {
		t.Fatalf("state = %q, want active: fenced attempt records must not leak", status.State)
	}
}

func TestAppendWake(t *testing.T) {
	home := useHome(t)
	if err := AppendWake("task_a", "ready: result published (attempt 1)"); err != nil {
		t.Fatal(err)
	}
	if err := AppendWake("task_a", "verified: attempt 1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(WakePath(home, "task_a"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "ready: result published") ||
		!strings.Contains(lines[1], "verified") {
		t.Fatalf("wake file = %q", data)
	}
}

func TestLockMutualExclusionAndRelease(t *testing.T) {
	useHome(t)
	release, err := Acquire(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	// The live current pid must refuse a second acquisition.
	if _, err := Acquire(context.Background(), "second"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	release()
	// After release the lock is available again.
	releaseAgain, err := Acquire(context.Background(), "third")
	if err != nil {
		t.Fatal(err)
	}
	releaseAgain()
}

func TestLockReclaimsDeadOwner(t *testing.T) {
	home := useHome(t)
	// Run a child to completion so its pid is definitively dead.
	command := exec.Command("sleep", "0.01")
	if err := command.Run(); err != nil {
		t.Skipf("cannot run child process: %v", err)
	}
	deadPID := command.Process.Pid
	lockDir := LockDir(home)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Publish(filepath.Join(lockDir, "owner.json"),
		LockOwner{PID: deadPID, Command: "crashed", AcquiredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(context.Background(), "reclaimer")
	if err != nil {
		t.Fatalf("dead owner was not reclaimed: %v", err)
	}
	release()
}

func TestLockRefusesUnreadableOwner(t *testing.T) {
	home := useHome(t)
	lockDir := LockDir(home)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), "probe"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want conservative ErrLocked", err)
	}
}

func TestCommanderRegistrationRoundTrip(t *testing.T) {
	home := useHome(t)
	if _, err := ReadCommander(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadCommander without registration = %v, want ErrNotFound", err)
	}
	registration := CommanderRegistration{
		Session: "fm-lab-x", WorkspaceID: "w9", TabID: "w9:t1", PaneID: "w9:p1",
		Runtime: "claude", AttachedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := PublishCommander(registration); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadCommander()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != registration {
		t.Fatalf("loaded = %+v, want %+v", loaded, registration)
	}
	// A fresh attach atomically replaces only the volatile address.
	replacement := CommanderRegistration{Session: "fm-lab-y", PaneID: "w2:p1", AttachedAt: registration.AttachedAt}
	if err := PublishCommander(replacement); err != nil {
		t.Fatal(err)
	}
	loaded, err = ReadCommander()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != replacement {
		t.Fatalf("replaced registration = %+v, want %+v", loaded, replacement)
	}
	if _, err := os.Stat(CommanderPath(home)); err != nil {
		t.Fatal(err)
	}
}
