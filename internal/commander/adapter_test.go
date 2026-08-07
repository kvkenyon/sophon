package commander

import (
	"context"
	"testing"

	"parallel-intellect/internal/herdr"
)

type fakeTerminal struct {
	startRequest herdr.StartRequest
	submissions  []string
	state        herdr.State
	cancels      int
	stops        int
}

func (f *fakeTerminal) Start(_ context.Context, request herdr.StartRequest) (herdr.Session, error) {
	f.startRequest = request
	return herdr.Session{Runtime: request.Runtime, AgentName: request.AgentName, AgentSessionID: "native-session",
		SessionName: "fm-lab", WorkspaceID: "w1", TabID: "t1", PaneID: "p1",
		WorktreePath: request.WorktreePath, Model: request.Model, PiExtensionPath: request.PiExtensionPath}, nil
}
func (f *fakeTerminal) Observe(context.Context, herdr.Session) (herdr.State, error) {
	return f.state, nil
}
func (f *fakeTerminal) Submit(_ context.Context, session herdr.Session, message string) (herdr.Session, error) {
	f.submissions = append(f.submissions, message)
	return session, nil
}
func (f *fakeTerminal) Cancel(context.Context, herdr.Session) error { f.cancels++; return nil }
func (f *fakeTerminal) Stop(context.Context, herdr.Session) error   { f.stops++; return nil }

func TestHerdrAdapterCommanderSurface(t *testing.T) {
	for _, runtime := range []herdr.Runtime{herdr.RuntimePi, herdr.RuntimeClaude, herdr.RuntimeCodex} {
		t.Run(string(runtime), func(t *testing.T) {
			terminal := &fakeTerminal{state: herdr.StateRunning}
			adapter := HerdrAdapter{Terminal: terminal}
			session, err := adapter.Start(context.Background(), StartConfig{SessionID: "csn_1", MissionID: "msn_1",
				Runtime: runtime, WorkingDir: "/project", Prompt: "mission context", Model: "model",
				PiExtensionPath: "/state/pi.ts"})
			if err != nil {
				t.Fatal(err)
			}
			if terminal.startRequest.Runtime != runtime || terminal.startRequest.AgentName != "pi-commander-msn_1" {
				t.Fatalf("start request = %+v", terminal.startRequest)
			}
			for name, operation := range map[string]func(context.Context, Session, string) (Session, error){
				"prompt": adapter.Prompt, "steer": adapter.Steer, "follow_up": adapter.FollowUp,
			} {
				if _, err := operation(context.Background(), session, name); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
			if state, err := adapter.State(context.Background(), session); err != nil || state != StateRunning {
				t.Fatalf("state = %s, %v", state, err)
			}
			if err := adapter.Abort(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			if len(terminal.submissions) != 3 || terminal.cancels != 1 || terminal.stops != 1 {
				t.Fatalf("submissions=%v cancels=%d stops=%d", terminal.submissions, terminal.cancels, terminal.stops)
			}
		})
	}
}
