// Package digest builds the regenerable semantic mission index from durable
// source material. It intentionally owns no authoritative state.
package digest

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"parallel-intellect/internal/domain"
	signalpolicy "parallel-intellect/internal/signals"
)

type Artifact struct {
	ID                   domain.ArtifactID `json:"id"`
	MissionID            domain.MissionID  `json:"mission_id"`
	Kind                 string            `json:"kind"`
	MediaType            string            `json:"media_type"`
	SHA256               string            `json:"sha256"`
	Content              string            `json:"content"`
	BasedOnEventSequence int64             `json:"based_on_event_sequence"`
	CreatedBy            string            `json:"created_by"`
	CreatedAt            time.Time         `json:"created_at"`
}

type Input struct {
	Mission domain.Mission
	Tasks   []domain.Task
	Signals []signalpolicy.Signal
	Events  []domain.Event
}

func Render(in Input) []byte {
	completed := make([]string, 0)
	remaining := make([]string, 0)
	for _, task := range in.Tasks {
		line := task.Title + " (" + string(task.State) + ")"
		switch task.State {
		case domain.TaskDelivered, domain.TaskDeliveredBranch, domain.TaskReportReady:
			completed = append(completed, line)
		case domain.TaskCancelled:
			// Cancelled work is no longer remaining and is not an accomplishment.
		default:
			remaining = append(remaining, line)
		}
	}
	decisions := make([]string, 0)
	openSignals := make([]string, 0)
	for _, signal := range in.Signals {
		if signal.Status == signalpolicy.SignalResolved && signal.Answer != nil {
			decisions = append(decisions, signal.Question+": "+*signal.Answer)
		} else if signal.Status == signalpolicy.SignalOpen {
			openSignals = append(openSignals, signal.Question)
		}
	}
	// Synthetic or imported event streams may contain resolved decisions before
	// the signal projection is available. Preserve those answers as provenance.
	for _, event := range in.Events {
		if event.Type != "signal.resolved" {
			continue
		}
		var payload struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && strings.TrimSpace(payload.Answer) != "" {
			found := false
			for _, decision := range decisions {
				if strings.HasSuffix(decision, ": "+payload.Answer) || decision == payload.Answer {
					found = true
				}
			}
			if !found {
				decisions = append(decisions, payload.Answer)
			}
		}
	}
	sort.Strings(decisions)
	sort.Strings(completed)
	sort.Strings(remaining)
	sort.Strings(openSignals)

	var body bytes.Buffer
	section(&body, "Objective", []string{in.Mission.Objective})
	section(&body, "Decisions", decisions)
	section(&body, "Completed", completed)
	section(&body, "Remaining", remaining)
	section(&body, "Open Signals", openSignals)
	return body.Bytes()
}

func section(body *bytes.Buffer, title string, items []string) {
	body.WriteString("## " + title + "\n\n")
	if len(items) == 0 {
		body.WriteString("None.\n\n")
		return
	}
	for _, item := range items {
		body.WriteString("- " + strings.TrimSpace(item) + "\n")
	}
	body.WriteByte('\n')
}
