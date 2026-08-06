package worker

import (
	"context"
	"testing"

	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
)

type cancelLeases struct {
	calls   int
	taskID  domain.TaskID
	attempt int
}

func (l *cancelLeases) Release(_ context.Context, _ domain.CommandID, taskID domain.TaskID, attempt int) (domain.TreehouseLease, error) {
	l.calls++
	l.taskID, l.attempt = taskID, attempt
	return domain.TreehouseLease{}, nil
}

type cancelHerdr struct {
	state herdr.State
	stops int
}

func (h *cancelHerdr) StartCodex(context.Context, herdr.StartRequest) (herdr.Session, error) {
	return herdr.Session{}, nil
}
func (h *cancelHerdr) Observe(context.Context, herdr.Session) (herdr.State, error) {
	return h.state, nil
}
func (h *cancelHerdr) Wake(_ context.Context, s herdr.Session, _ string) (herdr.Session, error) {
	return s, nil
}
func (h *cancelHerdr) Stop(context.Context, herdr.Session) error { h.stops++; return nil }

func TestCancelReleasesCurrentLeaseAndStopsLiveSession(t *testing.T) {
	store, task, _ := setupRunningWorker(t)
	leases := &cancelLeases{}
	runtime := &cancelHerdr{state: herdr.StateRunning}
	cancelled, err := (&Canceller{Store: store, Treehouse: leases, Herdr: runtime}).Cancel(context.Background(), task.ID, "cmd_cancel_live")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != domain.TaskCancelled || leases.calls != 1 || leases.taskID != task.ID || leases.attempt != 1 || runtime.stops != 1 {
		t.Fatalf("cancelled=%+v releases=%+v stops=%d", cancelled, leases, runtime.stops)
	}
	session, err := store.WorkerSession(context.Background(), task.ID, 1)
	if err != nil || session.State != domain.WorkerSessionStopped {
		t.Fatalf("session=%+v err=%v", session, err)
	}
}

func TestCancelWithDeadSessionStillCancels(t *testing.T) {
	store, task, _ := setupRunningWorker(t)
	runtime := &cancelHerdr{state: herdr.StateLost}
	cancelled, err := (&Canceller{Store: store, Treehouse: &cancelLeases{}, Herdr: runtime}).Cancel(context.Background(), task.ID, "cmd_cancel_dead")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != domain.TaskCancelled || runtime.stops != 0 {
		t.Fatalf("cancelled=%+v stops=%d", cancelled, runtime.stops)
	}
	events, err := store.TaskEvents(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-2].Type != "task.cancelling" || events[len(events)-1].Type != "task.cancelled" {
		t.Fatalf("cancel events=%+v", events[len(events)-2:])
	}
}
