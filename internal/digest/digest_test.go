package digest

import (
	"encoding/json"
	"strings"
	"testing"

	"sophon/internal/domain"
)

func TestRenderRegeneratesSemanticIndexFromSyntheticEventStream(t *testing.T) {
	answer, _ := json.Marshal(map[string]string{"answer": "Preserve the response contract."})
	in := Input{
		Mission: domain.Mission{Objective: "Improve invitation reliability."},
		Tasks: []domain.Task{
			{Title: "Fix duplicate consumption", State: domain.TaskDelivered},
			{Title: "Investigate slowdown", State: domain.TaskRunning},
		},
		Events: []domain.Event{{Sequence: 7, Type: "signal.resolved", Payload: answer}},
	}
	first := string(Render(in))
	second := string(Render(in))
	for _, expected := range []string{"## Objective", "Improve invitation reliability.",
		"## Decisions", "Preserve the response contract.", "Fix duplicate consumption (delivered)",
		"Investigate slowdown (running)", "## Open Signals\n\nNone."} {
		if !strings.Contains(first, expected) {
			t.Fatalf("digest missing %q:\n%s", expected, first)
		}
	}
	if first != second {
		t.Fatal("identical authoritative inputs produced a different digest")
	}
}
