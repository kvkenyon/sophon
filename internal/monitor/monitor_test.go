package monitor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"sophon/internal/domain"
	"sophon/internal/herdr"
	"sophon/internal/store"
)

type eventSink struct {
	mu     sync.Mutex
	events []Event
	wake   chan struct{}
}

func newEventSink() *eventSink { return &eventSink{wake: make(chan struct{}, 64)} }

func (s *eventSink) Forward(_ context.Context, event Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	s.wake <- struct{}{}
	return nil
}

func (s *eventSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "sophon-monitor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("SOPHON_DATA_HOME", home)
	return home
}

func canonicalTask(t *testing.T, home, taskID string) store.Task {
	t.Helper()
	mission := store.Mission{ID: "mission_a", ProjectPath: "/tmp/project", Title: "test", Objective: "test", CreatedAt: time.Now().UTC()}
	if _, err := os.Stat(store.MissionPath(home, mission.ID)); errors.Is(err, os.ErrNotExist) {
		if err := store.CreateMission(mission); err != nil {
			t.Fatal(err)
		}
	}
	task := store.Task{ID: taskID, MissionID: mission.ID, Title: "Public task", Objective: "Exercise monitor transport",
		DeliveryBranch: "test/monitor-" + taskID, Kind: domain.TaskImplementation,
		DeliveryMode: domain.DeliveryBranch, CreatedAt: time.Now().UTC()}
	if err := store.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	task, err := store.BumpAttempt(mission.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	spawn := store.Spawn{TaskID: task.ID, MissionID: mission.ID, Attempt: 1, WorktreePath: "/tmp/worktree",
		Branch: "task/test", BaseSHA: "base", LeaseID: "lease", LeaseHolder: "holder",
		Pane: herdr.Session{SessionName: "fm-lab-test", PaneID: "w1:p1"}, AgentRuntime: "codex", StartedAt: time.Now().UTC()}
	if err := store.Publish(store.AttemptPath(home, mission.ID, task.ID, 1, "spawn.json"), spawn); err != nil {
		t.Fatal(err)
	}
	return task
}

func publishResult(t *testing.T, home string, task store.Task) string {
	t.Helper()
	result := domain.WorkerResult{Version: 1, Status: "completed", Summary: "done",
		Verification: []domain.VerificationResult{{Command: "go test ./...", ExitCode: 0}},
		ChangedFiles: []string{"change.go"}, Risks: []string{}}
	if err := store.Publish(store.AttemptPath(home, task.MissionID, task.ID, 1, "result.json"), result); err != nil {
		t.Fatal(err)
	}
	generation, err := CanonicalGeneration(home, task.ID, 1, ChangeCompletion)
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func startServer(t *testing.T, home string, sink Forwarder, delay time.Duration) (*Client, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	server := &Server{Home: home, Forwarder: sink, CoalesceDelay: delay}
	go func() { errs <- server.Run(ctx) }()
	client := NewClient(home)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := client.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("server did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		_, _ = client.Shutdown()
		cancel()
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("monitor shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("monitor failed to stop")
		}
	})
	return client, errs
}

func TestProgressIsValidatedFencedSanitizedAndCoalesced(t *testing.T) {
	home := shortHome(t)
	canonicalTask(t, home, "task_a")
	canonicalTask(t, home, "task_b")
	sink := newEventSink()
	client, _ := startServer(t, home, sink, 80*time.Millisecond)

	first, err := client.Progress("task_a", 1, PhaseImplementing, "bounded note")
	if err != nil || first.Status != AckAccepted {
		t.Fatalf("first progress = %+v, %v", first, err)
	}
	second, err := client.Progress("task_a", 1, PhaseTesting, "tests\nstarted")
	if err != nil || second.Status != AckCoalesced {
		t.Fatalf("second progress = %+v, %v", second, err)
	}
	other, err := client.Progress("task_b", 1, PhaseInvestigating, "other")
	if err != nil || other.Status != AckAccepted {
		t.Fatalf("other progress = %+v, %v", other, err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-sink.wake:
		case <-time.After(time.Second):
			t.Fatal("coalesced event was not forwarded")
		}
	}
	events := sink.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	byTask := map[string]Event{}
	for _, event := range events {
		byTask[event.TaskID] = event
	}
	if byTask["task_a"].Phase != PhaseTesting || byTask["task_a"].Note != "tests started" {
		t.Fatalf("strongest task_a event = %+v", byTask["task_a"])
	}

	if ack, err := client.Progress("task_a", 2, PhaseTesting, "stale"); err != nil || ack.Status != AckRejected {
		t.Fatalf("stale progress = %+v, %v", ack, err)
	}
	if ack, err := client.Progress("task_a", 1, "chatting", "bad"); err != nil || ack.Status != AckRejected {
		t.Fatalf("unknown phase = %+v, %v", ack, err)
	}
	if ack, err := client.Progress("task_b", 1, PhaseTesting, "about to be fenced"); err != nil || ack.Status != AckAccepted {
		t.Fatalf("pre-fence progress = %+v, %v", ack, err)
	}
	taskB, err := store.FindTask("task_b")
	if err != nil {
		t.Fatal(err)
	}
	taskB.CurrentAttempt = 2
	if err := store.Publish(store.TaskPath(home, taskB.MissionID, taskB.ID), taskB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * 80 * time.Millisecond)
	if events := sink.snapshot(); len(events) != 2 {
		t.Fatalf("fenced progress was forwarded: %+v", events)
	}
}

func TestTaskChangedRequiresExactCanonicalGenerationAndDominatesProgress(t *testing.T) {
	home := shortHome(t)
	task := canonicalTask(t, home, "task_a")
	generation := publishResult(t, home, task)
	sink := newEventSink()
	client, _ := startServer(t, home, sink, 100*time.Millisecond)

	if ack, err := client.Progress(task.ID, 1, PhaseTesting, "finishing"); err != nil || ack.Status != AckAccepted {
		t.Fatalf("progress = %+v, %v", ack, err)
	}
	if ack, err := client.TaskChanged(task.ID, 1, ChangeCompletion, generation); err != nil || ack.Status != AckCoalesced {
		t.Fatalf("task change = %+v, %v", ack, err)
	}
	select {
	case <-sink.wake:
	case <-time.After(time.Second):
		t.Fatal("task change was not forwarded")
	}
	events := sink.snapshot()
	if len(events) != 1 || events[0].Kind != MethodTaskChanged || events[0].Change != ChangeCompletion {
		t.Fatalf("forwarded events = %+v", events)
	}
	bad := fmt.Sprintf("%064d", 0)
	if ack, err := client.TaskChanged(task.ID, 1, ChangeCompletion, bad); err != nil || ack.Status != AckRejected {
		t.Fatalf("bad generation = %+v, %v", ack, err)
	}
	if ack, err := client.TaskChanged(task.ID, 1, ChangeValidation, generation); err != nil || ack.Status != AckRejected {
		t.Fatalf("missing validation = %+v, %v", ack, err)
	}
}

func TestEveryTaskChangeKindRequiresTypedCanonicalEvidence(t *testing.T) {
	home := shortHome(t)
	now := time.Now().UTC()
	tests := []struct {
		id     string
		change string
		value  func(store.Task) any
	}{
		{"task_completion", ChangeCompletion, func(store.Task) any {
			return domain.WorkerResult{Version: 1, Status: "completed", Summary: "done",
				Verification: []domain.VerificationResult{{Command: "test", ExitCode: 0}},
				ChangedFiles: []string{"x"}, Risks: []string{}}
		}},
		{"task_report", ChangeReport, func(task store.Task) any {
			return store.WorkerReport{Version: 1, Status: store.WorkerReportBlocked, TaskID: task.ID, Attempt: 1,
				HeadSHA: "head", Reason: "blocked", Verification: []domain.VerificationResult{},
				Evidence: []string{"evidence"}, ChangedFiles: []string{}, Risks: []string{}}
		}},
		{"task_verification", ChangeVerification, func(task store.Task) any {
			return store.Outcome{TaskID: task.ID, Attempt: 1, HeadSHA: "head", Branch: "branch",
				ResultSHA256: fmt.Sprintf("%064d", 1), VerifiedAt: now}
		}},
		{"task_validation", ChangeValidation, func(task store.Task) any {
			return store.Validation{TaskID: task.ID, Attempt: 1, Command: "test", HeadSHA: "head", Passed: true, RanAt: now}
		}},
		{"task_delivery", ChangeDelivery, func(task store.Task) any {
			return store.Delivery{TaskID: task.ID, Attempt: 1, Mode: domain.DeliveryBranch, Branch: "branch",
				HeadSHA: "head", State: store.DeliveryDeliveredBranch, IntentAt: now, DeliveredAt: &now}
		}},
		{"task_release", ChangeRelease, func(task store.Task) any {
			return store.Release{TaskID: task.ID, Attempt: 1, LeaseID: "lease", LeaseHolder: "holder", ReleasedAt: now}
		}},
	}
	for _, test := range tests {
		t.Run(test.change, func(t *testing.T) {
			task := canonicalTask(t, home, test.id)
			path, err := canonicalPath(home, task, 1, test.change)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Publish(path, test.value(task)); err != nil {
				t.Fatal(err)
			}
			generation, err := CanonicalGeneration(home, task.ID, 1, test.change)
			if err != nil {
				t.Fatal(err)
			}
			params := TaskChangedParams{TaskID: task.ID, Attempt: 1, Change: test.change, ChangeGeneration: generation}
			if err := validateCanonicalChange(home, params); err != nil {
				t.Fatalf("validate %s: %v", test.change, err)
			}
		})
	}
}

func TestReviewChangeNotificationHashesLatestImmutableEventOnly(t *testing.T) {
	home := shortHome(t)
	task := canonicalTask(t, home, "task_review")
	binding := store.ReviewBinding{Version: 1, Product: store.ReviewProduct, ProductSchemaVersion: 1,
		TaskID: task.ID, Attempt: 1, SessionID: "57d91f3ddc544f34e70c1156",
		BaseSHA: "1111111111111111111111111111111111111111", HeadSHA: "2222222222222222222222222222222222222222",
		OpenedAt: time.Now().UTC()}
	if err := store.PublishReviewBindingForTask(task, binding); err != nil {
		t.Fatal(err)
	}
	event := store.ReviewEvent{Version: 1, ProductSchema: 1, TaskID: task.ID, Attempt: 1,
		SessionID: binding.SessionID, Sequence: 1, ProductEventID: "11111111-1111-4111-8111-111111111111",
		Type: "approval", CreatedAt: time.Now().UTC(), BaseSHA: binding.BaseSHA, HeadSHA: binding.HeadSHA,
		ApprovedHeadSHA: binding.HeadSHA}
	if err := store.PublishReviewEvent(task, binding, event); err != nil {
		t.Fatal(err)
	}
	generation, err := CanonicalGeneration(home, task.ID, 1, ChangeReview)
	if err != nil {
		t.Fatal(err)
	}
	params := TaskChangedParams{TaskID: task.ID, Attempt: 1, Change: ChangeReview, ChangeGeneration: generation}
	if err := validateCanonicalChange(home, params); err != nil {
		t.Fatal(err)
	}
	// Comment content is never present in the notification envelope; only the
	// fixed change kind and digest cross the monitor transport.
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "approval") || strings.Contains(string(encoded), binding.HeadSHA) {
		t.Fatalf("review content leaked into monitor params: %s", encoded)
	}
}

func TestPendingQueueIsBounded(t *testing.T) {
	server := &Server{Forwarder: ForwarderFunc(func(context.Context, Event) error { return nil }), CoalesceDelay: time.Hour,
		pending: make(map[string]*pendingEvent), Logger: log.New(io.Discard, "", 0)}
	for i := 0; i < maxPending; i++ {
		ack := server.enqueue(Event{Kind: MethodProgress, TaskID: fmt.Sprintf("task_%d", i), Attempt: 1,
			Phase: PhaseInvestigating})
		if ack.Status != AckAccepted {
			t.Fatalf("queue item %d = %+v", i, ack)
		}
	}
	overflow := server.enqueue(Event{Kind: MethodProgress, TaskID: "task_overflow", Attempt: 1,
		Phase: PhaseInvestigating})
	if overflow.Status != AckRejected {
		t.Fatalf("overflow = %+v", overflow)
	}
	server.flushPending()
	if len(server.pending) != 0 {
		t.Fatalf("pending queue retained %d items", len(server.pending))
	}
}

func TestSanitizeNoteBoundsControlsAndUTF8(t *testing.T) {
	note := "hello\n\tworld\x00 " + strings.Repeat("界", 200)
	clean := SanitizeNote(note)
	if len(clean) > MaxNoteLength || !strings.HasPrefix(clean, "hello world ") || !utf8.ValidString(clean) || strings.ContainsAny(clean, "\n\t\x00") {
		t.Fatalf("sanitized note length=%d value=%q", len(clean), clean)
	}
}

func rawRequest(t *testing.T, home string, value any) response {
	t.Helper()
	conn, err := net.Dial("unix", SocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeFrame(conn, value); err != nil {
		t.Fatal(err)
	}
	data, err := readFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	var resp response
	if err := decodeStrict(data, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestProtocolRejectsUnknownFieldsReplayOversizeAndSlowClients(t *testing.T) {
	home := shortHome(t)
	canonicalTask(t, home, "task_a")
	client, _ := startServer(t, home, newEventSink(), 20*time.Millisecond)
	record, err := readRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(PingParams{ProtocolVersion: ProtocolVersion, Generation: record.Generation})
	req := request{JSONRPC: JSONRPCVersion, Method: MethodPing, ID: "same-id", Params: params}
	first := rawRequest(t, home, req)
	if first.Error != nil {
		t.Fatalf("first request = %+v", first)
	}
	second := rawRequest(t, home, req)
	var replay Ack
	if err := json.Unmarshal(second.Result, &replay); err != nil || replay.Status != AckRejected {
		t.Fatalf("replay = %+v / %+v / %v", second, replay, err)
	}

	unknown := map[string]any{"jsonrpc": "2.0", "method": MethodPing, "id": "unknown", "params": map[string]any{}, "extra": true}
	if resp := rawRequest(t, home, unknown); resp.Error == nil || resp.Error.Code != -32700 {
		t.Fatalf("unknown-field response = %+v", resp)
	}

	oversized, err := net.Dial("unix", SocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	if _, err := oversized.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	data, err := readFrame(oversized)
	oversized.Close()
	if err != nil {
		t.Fatal(err)
	}
	var oversizedResponse response
	if err := decodeStrict(data, &oversizedResponse); err != nil || oversizedResponse.Error == nil {
		t.Fatalf("oversized response = %+v, %v", oversizedResponse, err)
	}

	slow, err := net.Dial("unix", SocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slow.Write([]byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(clientDeadline + 100*time.Millisecond)
	_ = slow.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = readFrame(slow)
	slow.Close()
	if _, err := client.Ping(); err != nil {
		t.Fatalf("slow client impaired monitor: %v", err)
	}
}

func TestStaleRecoveryRequiresDeadPIDAndExactSocket(t *testing.T) {
	home := shortHome(t)
	task := canonicalTask(t, home, "task_crash")
	publishResult(t, home, task)
	if err := EnsureRuntimeDir(home); err != nil {
		t.Fatal(err)
	}
	generation, _ := newGeneration()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: SocketPath(home), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close() // hard-crash shape: socket inode remains, listener is gone
	if err := os.Chmod(SocketPath(home), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishRuntime(home, RuntimeRecord{Version: runtimeVersion, Generation: generation,
		PID: 1<<30 - 1, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	client, _ := startServer(t, home, newEventSink(), 20*time.Millisecond)
	if _, err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	current, err := readRuntime(home)
	if err != nil || current.Generation == generation {
		t.Fatalf("runtime was not safely regenerated: %+v, %v", current, err)
	}
	canonical, err := store.FindTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Derive(canonical)
	if err != nil || status.State != store.StateReady {
		t.Fatalf("canonical completion did not survive monitor crash/restart: %+v, %v", status, err)
	}
}

func TestPIDReuseShapeIsConservativeAndDoesNotDeleteIdentity(t *testing.T) {
	home := shortHome(t)
	if err := EnsureRuntimeDir(home); err != nil {
		t.Fatal(err)
	}
	generation, _ := newGeneration()
	record := RuntimeRecord{Version: runtimeVersion, Generation: generation, PID: os.Getpid(), StartedAt: time.Now().UTC()}
	if err := publishRuntime(home, record); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home, Forwarder: newEventSink()}
	err := server.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "alive") {
		t.Fatalf("server error = %v, want conservative live-pid refusal", err)
	}
	current, readErr := readRuntime(home)
	if readErr != nil || current.Generation != generation {
		t.Fatalf("ambiguous runtime was changed: %+v, %v", current, readErr)
	}
}

func TestConcurrentServersConvergeAndContextShutdownCleansExactFiles(t *testing.T) {
	home := shortHome(t)
	if err := EnsureRuntimeDir(home); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		server := &Server{Home: home, Forwarder: newEventSink(), CoalesceDelay: 10 * time.Millisecond}
		go func() { errs <- server.Run(ctx) }()
	}
	client := NewClient(home)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := client.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("neither concurrent server became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var already error
	select {
	case already = <-errs:
	case <-time.After(2 * time.Second):
		t.Fatal("losing concurrent server did not converge")
	}
	if !errors.Is(already, ErrAlreadyRunning) {
		t.Fatalf("losing server = %v", already)
	}
	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("running server did not stop on context cancellation")
	}
	if _, err := os.Lstat(SocketPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after signal-equivalent shutdown: %v", err)
	}
	if _, err := os.Lstat(RuntimePath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime remains after signal-equivalent shutdown: %v", err)
	}
}

func TestRuntimeDirectoryAndSocketArePrivateAndLongHomesUseExactAlias(t *testing.T) {
	home := shortHome(t)
	client, _ := startServer(t, home, newEventSink(), 10*time.Millisecond)
	if _, err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{RuntimeDir(home), RuntimePath(home), SocketPath(home)} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	longHome := "/tmp/" + fmt.Sprintf("%0100d", 1)
	if err := EnsureRuntimeDir(longHome); err != nil {
		t.Fatalf("long data home was not safely supported: %v", err)
	}
	address, err := socketAddress(longHome)
	if err != nil || len(address) >= 104 {
		t.Fatalf("long-home address = %q, %v", address, err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(address))
	want, wantErr := filepath.EvalSymlinks(RuntimeDir(longHome))
	if err != nil || wantErr != nil || resolved != want {
		t.Fatalf("long-home alias resolves to %q, %v; want %q (%v)", resolved, err, want, wantErr)
	}
	cleanupSocketAlias(longHome)
	_ = os.RemoveAll(longHome)
}

func TestSymlinkRuntimeTargetsAreNeverFollowedOrRemoved(t *testing.T) {
	home := shortHome(t)
	if err := EnsureRuntimeDir(home); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(home, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, SocketPath(home)); err != nil {
		t.Fatal(err)
	}
	server := &Server{Home: home, Forwarder: newEventSink()}
	if err := server.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "non-socket or symlink") {
		t.Fatalf("socket symlink error = %v", err)
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatalf("socket symlink victim changed: %q, %v", data, err)
	}
	if err := os.Remove(SocketPath(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(LockPath(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, LockPath(home)); err != nil {
		t.Fatal(err)
	}
	if err := server.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "start lock path") {
		t.Fatalf("lock symlink error = %v", err)
	}
	data, err = os.ReadFile(victim)
	if err != nil || string(data) != "keep" {
		t.Fatalf("lock symlink victim changed: %q, %v", data, err)
	}
}

func TestClientCannotRouteAcrossDataHomes(t *testing.T) {
	home := shortHome(t)
	client, _ := startServer(t, home, newEventSink(), 10*time.Millisecond)
	if _, err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	other, err := os.MkdirTemp("/tmp", "sophon-monitor-other-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(other)
	if _, err := NewClient(other).Ping(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cross-data-home ping = %v", err)
	}
}
