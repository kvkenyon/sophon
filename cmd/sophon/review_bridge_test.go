package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sophon/internal/domain"
	"sophon/internal/monitor"
	"sophon/internal/reviewbridge"
	"sophon/internal/store"
)

func TestReviewBridgePublishesBeforeOneMonitorNotificationAndCancelsExactly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	mission := store.Mission{ID: "mission_bridge", ProjectPath: t.TempDir(), Title: "Bridge",
		Objective: "Prove the review bridge.", CreatedAt: time.Now().UTC()}
	if err := store.CreateMission(mission); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "task_bridge", MissionID: mission.ID, Title: "Review bridge",
		Objective: "Ingest exact review feedback.", DeliveryBranch: "review/bridge", Kind: domain.TaskImplementation,
		DeliveryMode: domain.DeliveryBranch, ReviewPosture: domain.ReviewRequired, CreatedAt: time.Now().UTC()}
	if err := store.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	var err error
	task, err = store.BumpAttempt(task.MissionID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := "1111111111111111111111111111111111111111"
	head := "2222222222222222222222222222222222222222"
	spawn := store.Spawn{TaskID: task.ID, MissionID: task.MissionID, Attempt: 1,
		WorktreePath: mission.ProjectPath, Branch: "sophon/review-bridge/attempt-1", BaseSHA: base,
		LeaseID: "lease-bridge", LeaseHolder: "sophon:task_bridge:1", StartedAt: time.Now().UTC()}
	if err := store.Publish(store.AttemptPath(home, task.MissionID, task.ID, 1, "spawn.json"), spawn); err != nil {
		t.Fatal(err)
	}
	binding := store.ReviewBinding{Version: 1, Product: store.ReviewProduct, ProductSchemaVersion: 1,
		TaskID: task.ID, Attempt: 1, SessionID: "57d91f3ddc544f34e70c1156",
		BaseSHA: base, HeadSHA: head, OpenedAt: time.Now().UTC()}
	if err := store.PublishReviewBindingForTask(task, binding); err != nil {
		t.Fatal(err)
	}

	events := make(chan monitor.Event, 2)
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	server := &monitor.Server{Home: home, CoalesceDelay: 5 * time.Millisecond,
		Logger: log.New(io.Discard, "", 0), Forwarder: monitor.ForwarderFunc(func(_ context.Context, event monitor.Event) error {
			events <- event
			return nil
		})}
	go func() { serverDone <- server.Run(monitorCtx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := monitor.NewClient(home).Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("monitor did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		stopMonitor()
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("monitor shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("monitor did not stop")
		}
	})

	product := filepath.Join(t.TempDir(), "read-the-code-axi")
	writeCLIFile(t, product, `#!/bin/sh
set -eu
case "$1" in
  poll)
    if [ "$4" = "0" ]; then
      printf '%s\n' '{"schemaVersion":1,"sessionId":"57d91f3ddc544f34e70c1156","after":0,"nextCursor":1,"timedOut":false,"events":[{"schemaVersion":1,"sessionId":"57d91f3ddc544f34e70c1156","sequence":1,"id":"11111111-1111-4111-8111-111111111111","createdAt":"2026-08-08T12:00:00Z","baseSha":"1111111111111111111111111111111111111111","headSha":"2222222222222222222222222222222222222222","type":"feedback","comments":[{"id":"22222222-2222-4222-8222-222222222222","scope":"general","body":"untrusted browser body","createdAt":"2026-08-08T12:00:00Z"}]}]}'
    else
      sleep 30
    fi
    ;;
  status)
    printf '%s\n' '{"schemaVersion":1,"sessionId":"57d91f3ddc544f34e70c1156","status":"open","stale":false,"approvalStale":false,"baseSha":"1111111111111111111111111111111111111111","headSha":"2222222222222222222222222222222222222222","summary":{"files":1,"additions":1,"deletions":0},"eventCount":1,"lastSequence":1,"updatedAt":"2026-08-08T12:00:00Z"}'
    ;;
  *) exit 64 ;;
esac
`, 0o700)
	herdrCalls := filepath.Join(t.TempDir(), "herdr-calls")
	herdr := filepath.Join(t.TempDir(), "herdr")
	writeCLIFile(t, herdr, "#!/bin/sh\nprintf called >> "+herdrCalls+"\nexit 1\n", 0o700)

	bridgeCtx, stopBridge := context.WithCancel(context.Background())
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- reviewBridge(bridgeCtx, []string{task.ID, "--attempt", "1", "--read-the-code", product,
			"--herdr", herdr, "--herdr-session", "fm-lab-review-bridge"})
	}()
	select {
	case event := <-events:
		if event.TaskID != task.ID || event.Attempt != 1 || event.Change != monitor.ChangeReview {
			t.Fatalf("monitor event = %+v", event)
		}
		canonical, err := store.ReadReviewEvents(task.MissionID, task.ID, 1)
		if err != nil || len(canonical) != 1 || canonical[0].Sequence != 1 {
			t.Fatalf("monitor fired before canonical publication: %+v, %v", canonical, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("review bridge did not emit monitor notification")
	}
	if _, err := os.Stat(herdrCalls); !os.IsNotExist(err) {
		t.Fatalf("accepted monitor notification also used direct fallback: %v", err)
	}
	stopBridge()
	select {
	case <-bridgeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("review bridge cancellation did not terminate its exact poll process")
	}
	if running, err := reviewbridge.Running(home, reviewbridge.Expected(home, binding)); err != nil || running {
		t.Fatalf("cancelled bridge retained ownership: running=%t err=%v", running, err)
	}
}
