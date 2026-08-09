package store

// This file owns Sophon's canonical side of the Read the Code integration.
// The product remains an external versioned CLI. These records are Sophon's
// lossless, task-bound normalization of product events, not a copy of the
// product's session store.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sophon/internal/domain"
)

const (
	ReviewRecordVersion = 1
	ReviewProduct       = "read-the-code"
	ReviewProductSchema = 1

	ReviewDispositionRequestedChanges = "requested-changes"
	ReviewDispositionNonActionable    = "non-actionable"

	MaxReviewCommentBytes = 20 << 10
	MaxReviewBatchBytes   = 100 << 10
	MaxReviewComments     = 100
	MaxReviewLine         = 10_000_000
)

var (
	reviewSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	reviewSessionID = regexp.MustCompile(`^[0-9a-f]{24}$`)
	reviewUUID      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	reviewHash      = regexp.MustCompile(`^[0-9a-f]{24}$`)
)

// ReviewPostureChange is one immutable, monotonic task-level escalation. A
// task can move off -> optional|required or optional -> required; it can
// never silently lower a delivery guard.
type ReviewPostureChange struct {
	Version   int                  `json:"version"`
	TaskID    string               `json:"task_id"`
	Sequence  int                  `json:"sequence"`
	From      domain.ReviewPosture `json:"from"`
	To        domain.ReviewPosture `json:"to"`
	ChangedAt time.Time            `json:"changed_at"`
}

// ReviewBinding binds one Sophon attempt to one exact product session and
// revision. It deliberately excludes the capability-bearing browser URL,
// repository path, executable path, server placement, and process identity.
type ReviewBinding struct {
	Version              int       `json:"version"`
	Product              string    `json:"product"`
	ProductSchemaVersion int       `json:"product_schema_version"`
	TaskID               string    `json:"task_id"`
	Attempt              int       `json:"attempt"`
	SessionID            string    `json:"session_id"`
	BaseSHA              string    `json:"base_sha"`
	HeadSHA              string    `json:"head_sha"`
	OpenedAt             time.Time `json:"opened_at"`
}

type ReviewRevision struct {
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
}

type ReviewAnchor struct {
	Revision       ReviewRevision `json:"revision"`
	Path           string         `json:"path"`
	Side           string         `json:"side"`
	StartLine      int            `json:"start_line"`
	EndLine        int            `json:"end_line"`
	ContextHash    string         `json:"context_hash"`
	EndContextHash string         `json:"end_context_hash"`
}

type ReviewComment struct {
	ID        string        `json:"id"`
	Scope     string        `json:"scope"`
	Body      string        `json:"body"`
	Path      string        `json:"path,omitempty"`
	Anchor    *ReviewAnchor `json:"anchor,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

// ReviewEvent is an immutable, lossless normalized product submission. Its
// canonical filename is its zero-padded sequence, so cursor truth is derived
// from a contiguous directory rather than a mutable offset.
type ReviewEvent struct {
	Version         int             `json:"version"`
	ProductSchema   int             `json:"product_schema_version"`
	TaskID          string          `json:"task_id"`
	Attempt         int             `json:"attempt"`
	SessionID       string          `json:"session_id"`
	Sequence        int             `json:"sequence"`
	ProductEventID  string          `json:"product_event_id"`
	Type            string          `json:"type"`
	CreatedAt       time.Time       `json:"created_at"`
	BaseSHA         string          `json:"base_sha"`
	HeadSHA         string          `json:"head_sha"`
	ApprovedHeadSHA string          `json:"approved_head_sha,omitempty"`
	Comments        []ReviewComment `json:"comments,omitempty"`
}

// ReviewDecision records the commander's classification of one feedback
// submission. Comment text remains untrusted data and is never copied here.
type ReviewDecision struct {
	Version        int       `json:"version"`
	TaskID         string    `json:"task_id"`
	Attempt        int       `json:"attempt"`
	SessionID      string    `json:"session_id"`
	Sequence       int       `json:"sequence"`
	ProductEventID string    `json:"product_event_id"`
	Disposition    string    `json:"disposition"`
	DecidedAt      time.Time `json:"decided_at"`
}

// ReviewRoute proves a requested-change classification was routed through
// the exact current worker steering boundary. It contains no comment text.
type ReviewRoute struct {
	Version   int       `json:"version"`
	TaskID    string    `json:"task_id"`
	Attempt   int       `json:"attempt"`
	SessionID string    `json:"session_id"`
	Sequence  int       `json:"sequence"`
	RoutedAt  time.Time `json:"routed_at"`
}

// ReviewApprovalAcknowledgement is commander awareness only. Delivery uses
// the product approval event itself and never this receipt as authority.
type ReviewApprovalAcknowledgement struct {
	Version   int       `json:"version"`
	TaskID    string    `json:"task_id"`
	Attempt   int       `json:"attempt"`
	SessionID string    `json:"session_id"`
	Sequence  int       `json:"sequence"`
	HeadSHA   string    `json:"head_sha"`
	SeenAt    time.Time `json:"seen_at"`
}

// ReviewStatus is bounded current operational review derivation. It contains
// no comment text, capability URL, local path, executable, or process data.
type ReviewStatus struct {
	Version                  int                  `json:"version"`
	TaskID                   string               `json:"task_id"`
	Attempt                  int                  `json:"attempt"`
	Posture                  domain.ReviewPosture `json:"posture"`
	State                    string               `json:"state"`
	SessionID                string               `json:"session_id,omitempty"`
	BaseSHA                  string               `json:"base_sha,omitempty"`
	HeadSHA                  string               `json:"head_sha,omitempty"`
	Cursor                   int                  `json:"cursor"`
	LatestFeedbackSequence   int                  `json:"latest_feedback_sequence,omitempty"`
	LatestApprovalSequence   int                  `json:"latest_approval_sequence,omitempty"`
	PendingFeedbackSequences []int                `json:"pending_feedback_sequences"`
	RequestedChangeSequences []int                `json:"requested_change_sequences"`
	UnroutedChangeSequences  []int                `json:"unrouted_change_sequences"`
	ApprovalAcknowledged     bool                 `json:"approval_acknowledged"`
	ApprovalEligible         bool                 `json:"approval_eligible"`
	Ended                    bool                 `json:"ended"`
	BridgeRunning            bool                 `json:"bridge_running"`
	Detail                   string               `json:"detail,omitempty"`
}

func ReviewDir(home, missionID, taskID string, attempt int) string {
	return filepath.Join(AttemptDir(home, missionID, taskID, attempt), "review")
}

func ReviewBindingPath(home, missionID, taskID string, attempt int) string {
	return filepath.Join(ReviewDir(home, missionID, taskID, attempt), "open.json")
}

func ReviewEventPath(home, missionID, taskID string, attempt, sequence int) string {
	return filepath.Join(ReviewDir(home, missionID, taskID, attempt), "events", fmt.Sprintf("%020d.json", sequence))
}

func ReviewDecisionPath(home, missionID, taskID string, attempt, sequence int) string {
	return filepath.Join(ReviewDir(home, missionID, taskID, attempt), "decisions", fmt.Sprintf("%020d.json", sequence))
}

func ReviewRoutePath(home, missionID, taskID string, attempt, sequence int) string {
	return filepath.Join(ReviewDir(home, missionID, taskID, attempt), "routes", fmt.Sprintf("%020d.json", sequence))
}

func ReviewApprovalAckPath(home, missionID, taskID string, attempt, sequence int) string {
	return filepath.Join(ReviewDir(home, missionID, taskID, attempt), "approval-acknowledgements", fmt.Sprintf("%020d.json", sequence))
}

func reviewPosturePath(home, missionID, taskID string, sequence int) string {
	return filepath.Join(TaskDir(home, missionID, taskID), "review-posture", fmt.Sprintf("%020d.json", sequence))
}

func IntakeReviewPosture(task Task) domain.ReviewPosture {
	if task.ReviewPosture == "" {
		return domain.ReviewOff
	}
	return task.ReviewPosture
}

func ReadReviewPostureChanges(task Task) ([]ReviewPostureChange, error) {
	homeDir, err := home()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(TaskDir(homeDir, task.MissionID, task.ID), "review-posture")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list review posture history: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	changes := make([]ReviewPostureChange, 0, len(entries))
	current := IntakeReviewPosture(task)
	for index, entry := range entries {
		sequence, ok := reviewSequenceName(entry.Name())
		if !ok || !regularReviewEntry(entry) || sequence != index+1 {
			return nil, fmt.Errorf("%w: review posture history is not a contiguous regular-file sequence", ErrInvalidEvidence)
		}
		var change ReviewPostureChange
		data, err := readReviewBytes(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if err := decodeStrict(data, &change); err != nil {
			return nil, err
		}
		if change.Version != ReviewRecordVersion || change.TaskID != task.ID || change.Sequence != sequence ||
			change.From != current || !validReviewEscalation(change.From, change.To) || change.ChangedAt.IsZero() {
			return nil, fmt.Errorf("%w: invalid review posture change %d", ErrInvalidEvidence, sequence)
		}
		changes = append(changes, change)
		current = change.To
	}
	return changes, nil
}

func EffectiveReviewPosture(task Task) (domain.ReviewPosture, error) {
	changes, err := ReadReviewPostureChanges(task)
	if err != nil {
		return "", err
	}
	posture := IntakeReviewPosture(task)
	if len(changes) > 0 {
		posture = changes[len(changes)-1].To
	}
	return posture, nil
}

func PublishReviewPostureChange(task Task, to domain.ReviewPosture, now time.Time) (ReviewPostureChange, error) {
	changes, err := ReadReviewPostureChanges(task)
	if err != nil {
		return ReviewPostureChange{}, err
	}
	from := IntakeReviewPosture(task)
	if len(changes) > 0 {
		from = changes[len(changes)-1].To
	}
	if !validReviewEscalation(from, to) {
		return ReviewPostureChange{}, fmt.Errorf("review posture cannot transition from %s to %s", from, to)
	}
	record := ReviewPostureChange{Version: ReviewRecordVersion, TaskID: task.ID,
		Sequence: len(changes) + 1, From: from, To: to, ChangedAt: now.UTC()}
	homeDir, err := home()
	if err != nil {
		return ReviewPostureChange{}, err
	}
	return record, publishImmutable(reviewPosturePath(homeDir, task.MissionID, task.ID, record.Sequence), record)
}

func validReviewEscalation(from, to domain.ReviewPosture) bool {
	return (from == domain.ReviewOff && (to == domain.ReviewOptional || to == domain.ReviewRequired)) ||
		(from == domain.ReviewOptional && to == domain.ReviewRequired)
}

func ReadReviewBinding(missionID, taskID string, attempt int) (ReviewBinding, error) {
	var binding ReviewBinding
	homeDir, err := home()
	if err != nil {
		return binding, err
	}
	data, err := readReviewBytes(ReviewBindingPath(homeDir, missionID, taskID, attempt))
	if err != nil {
		return binding, err
	}
	if err := decodeStrict(data, &binding); err != nil {
		return binding, err
	}
	if err := ValidateReviewBinding(binding, taskID, attempt); err != nil {
		return ReviewBinding{}, err
	}
	return binding, nil
}

func ValidateReviewBinding(binding ReviewBinding, taskID string, attempt int) error {
	if binding.Version != ReviewRecordVersion || binding.Product != ReviewProduct ||
		binding.ProductSchemaVersion != ReviewProductSchema || binding.TaskID != taskID ||
		binding.Attempt != attempt || !reviewSessionID.MatchString(binding.SessionID) ||
		!reviewSHA.MatchString(binding.BaseSHA) || !reviewSHA.MatchString(binding.HeadSHA) ||
		binding.BaseSHA == binding.HeadSHA || binding.OpenedAt.IsZero() {
		return fmt.Errorf("%w: invalid review binding identity or revision", ErrInvalidEvidence)
	}
	return nil
}

// PublishReviewBindingForTask avoids resolving a task twice at the flow
// boundary and makes the task/mission path identity explicit.
func PublishReviewBindingForTask(task Task, binding ReviewBinding) error {
	if binding.TaskID != task.ID {
		return fmt.Errorf("%w: review binding task mismatch", ErrInvalidEvidence)
	}
	if err := ValidateReviewBinding(binding, task.ID, binding.Attempt); err != nil {
		return err
	}
	homeDir, err := home()
	if err != nil {
		return err
	}
	return publishImmutable(ReviewBindingPath(homeDir, task.MissionID, task.ID, binding.Attempt), binding)
}
func ReadReviewEvents(missionID, taskID string, attempt int) ([]ReviewEvent, error) {
	homeDir, err := home()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(ReviewDir(homeDir, missionID, taskID, attempt), "events")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list review events: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	events := make([]ReviewEvent, 0, len(entries))
	var binding ReviewBinding
	if len(entries) > 0 {
		binding, err = ReadReviewBinding(missionID, taskID, attempt)
		if err != nil {
			return nil, err
		}
	}
	ids := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		sequence, ok := reviewSequenceName(entry.Name())
		if !ok || !regularReviewEntry(entry) || sequence != index+1 {
			return nil, fmt.Errorf("%w: review event cursor has a gap or unsafe entry", ErrInvalidEvidence)
		}
		data, err := readReviewBytes(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var event ReviewEvent
		if err := decodeStrict(data, &event); err != nil {
			return nil, err
		}
		if err := ValidateReviewEvent(event, binding); err != nil {
			return nil, err
		}
		if event.Sequence != sequence {
			return nil, fmt.Errorf("%w: review event filename does not match sequence", ErrInvalidEvidence)
		}
		if _, duplicate := ids[event.ProductEventID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate review product event id", ErrInvalidEvidence)
		}
		ids[event.ProductEventID] = struct{}{}
		events = append(events, event)
	}
	return events, nil
}

func ReviewCursor(missionID, taskID string, attempt int) (int, error) {
	events, err := ReadReviewEvents(missionID, taskID, attempt)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	return events[len(events)-1].Sequence, nil
}

func LatestReviewEventPath(homeDir string, task Task, attempt int) (string, error) {
	cursor, err := ReviewCursor(task.MissionID, task.ID, attempt)
	if err != nil {
		return "", err
	}
	if cursor == 0 {
		return "", fmt.Errorf("%w: review has no canonical event", ErrNotFound)
	}
	return ReviewEventPath(homeDir, task.MissionID, task.ID, attempt, cursor), nil
}

func PublishReviewEvent(task Task, binding ReviewBinding, event ReviewEvent) error {
	if err := ValidateReviewEvent(event, binding); err != nil {
		return err
	}
	if task.ID != event.TaskID || task.MissionID == "" {
		return fmt.Errorf("%w: review event task path mismatch", ErrInvalidEvidence)
	}
	homeDir, err := home()
	if err != nil {
		return err
	}
	return publishImmutable(ReviewEventPath(homeDir, task.MissionID, task.ID, event.Attempt, event.Sequence), event)
}

func ValidateReviewEvent(event ReviewEvent, binding ReviewBinding) error {
	if event.Version != ReviewRecordVersion || event.ProductSchema != ReviewProductSchema ||
		event.TaskID != binding.TaskID || event.Attempt != binding.Attempt ||
		event.SessionID != binding.SessionID || event.Sequence < 1 ||
		!reviewUUID.MatchString(event.ProductEventID) || event.CreatedAt.IsZero() ||
		event.BaseSHA != binding.BaseSHA || event.HeadSHA != binding.HeadSHA {
		return fmt.Errorf("%w: review event identity does not match binding", ErrInvalidEvidence)
	}
	switch event.Type {
	case "approval":
		if event.ApprovedHeadSHA != binding.HeadSHA || event.Comments != nil {
			return fmt.Errorf("%w: approval is not exact-head or contains comments", ErrInvalidEvidence)
		}
	case "end":
		if event.ApprovedHeadSHA != "" || event.Comments != nil {
			return fmt.Errorf("%w: end event contains unexpected review data", ErrInvalidEvidence)
		}
	case "feedback":
		if event.ApprovedHeadSHA != "" || len(event.Comments) < 1 || len(event.Comments) > MaxReviewComments {
			return fmt.Errorf("%w: feedback requires a bounded comment batch", ErrInvalidEvidence)
		}
		if err := validateReviewComments(event.Comments, binding); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown review event type %q", ErrInvalidEvidence, event.Type)
	}
	return nil
}

func validateReviewComments(comments []ReviewComment, binding ReviewBinding) error {
	ids := make(map[string]struct{}, len(comments))
	total := 0
	for _, comment := range comments {
		if !reviewUUID.MatchString(comment.ID) || comment.CreatedAt.IsZero() ||
			comment.Body == "" || len(comment.Body) > MaxReviewCommentBytes || !utf8.ValidString(comment.Body) ||
			hasUnsafeControl(comment.Body) {
			return fmt.Errorf("%w: review comment identity or text is invalid", ErrInvalidEvidence)
		}
		if _, duplicate := ids[comment.ID]; duplicate {
			return fmt.Errorf("%w: duplicate review comment id", ErrInvalidEvidence)
		}
		ids[comment.ID] = struct{}{}
		total += len(comment.Body)
		switch comment.Scope {
		case "general":
			if comment.Path != "" || comment.Anchor != nil {
				return fmt.Errorf("%w: general review comment has a path or anchor", ErrInvalidEvidence)
			}
		case "file":
			if !safeReviewPath(comment.Path) || comment.Anchor != nil {
				return fmt.Errorf("%w: file review comment has invalid path or anchor", ErrInvalidEvidence)
			}
		case "line":
			if !safeReviewPath(comment.Path) || comment.Anchor == nil || comment.Anchor.Path != comment.Path ||
				comment.Anchor.Revision.BaseSHA != binding.BaseSHA || comment.Anchor.Revision.HeadSHA != binding.HeadSHA ||
				(comment.Anchor.Side != "old" && comment.Anchor.Side != "new") || comment.Anchor.StartLine < 1 ||
				comment.Anchor.EndLine < comment.Anchor.StartLine || comment.Anchor.EndLine > MaxReviewLine ||
				!reviewHash.MatchString(comment.Anchor.ContextHash) ||
				!reviewHash.MatchString(comment.Anchor.EndContextHash) {
				return fmt.Errorf("%w: line review comment has invalid exact-revision anchor", ErrInvalidEvidence)
			}
		default:
			return fmt.Errorf("%w: unknown review comment scope %q", ErrInvalidEvidence, comment.Scope)
		}
	}
	if total > MaxReviewBatchBytes {
		return fmt.Errorf("%w: review comment batch exceeds %d bytes", ErrInvalidEvidence, MaxReviewBatchBytes)
	}
	return nil
}

func safeReviewPath(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.Contains(value, "\\") && !hasUnsafeControl(value) &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && value != ".." &&
		!strings.HasPrefix(value, "../")
}

func hasUnsafeControl(value string) bool {
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

func ReadReviewDecision(missionID, taskID string, attempt, sequence int) (ReviewDecision, error) {
	var record ReviewDecision
	homeDir, err := home()
	if err != nil {
		return record, err
	}
	data, err := readReviewBytes(ReviewDecisionPath(homeDir, missionID, taskID, attempt, sequence))
	if err != nil {
		return record, err
	}
	if err := decodeStrict(data, &record); err != nil {
		return ReviewDecision{}, err
	}
	if record.Version != ReviewRecordVersion || record.TaskID != taskID || record.Attempt != attempt ||
		record.Sequence != sequence || !reviewSessionID.MatchString(record.SessionID) ||
		!reviewUUID.MatchString(record.ProductEventID) ||
		(record.Disposition != ReviewDispositionRequestedChanges && record.Disposition != ReviewDispositionNonActionable) ||
		record.DecidedAt.IsZero() {
		return ReviewDecision{}, fmt.Errorf("%w: invalid review decision", ErrInvalidEvidence)
	}
	return record, nil
}

func PublishReviewDecision(task Task, record ReviewDecision) error {
	if record.Version != ReviewRecordVersion || record.TaskID != task.ID || record.Attempt < 1 || record.Sequence < 1 ||
		!reviewSessionID.MatchString(record.SessionID) || !reviewUUID.MatchString(record.ProductEventID) ||
		(record.Disposition != ReviewDispositionRequestedChanges && record.Disposition != ReviewDispositionNonActionable) ||
		record.DecidedAt.IsZero() {
		return fmt.Errorf("%w: invalid review decision", ErrInvalidEvidence)
	}
	homeDir, err := home()
	if err != nil {
		return err
	}
	return publishImmutable(ReviewDecisionPath(homeDir, task.MissionID, task.ID, record.Attempt, record.Sequence), record)
}

func ReadReviewRoute(missionID, taskID string, attempt, sequence int) (ReviewRoute, error) {
	var record ReviewRoute
	homeDir, err := home()
	if err != nil {
		return record, err
	}
	data, err := readReviewBytes(ReviewRoutePath(homeDir, missionID, taskID, attempt, sequence))
	if err != nil {
		return record, err
	}
	if err := decodeStrict(data, &record); err != nil {
		return ReviewRoute{}, err
	}
	if record.Version != ReviewRecordVersion || record.TaskID != taskID || record.Attempt != attempt ||
		record.Sequence != sequence || !reviewSessionID.MatchString(record.SessionID) || record.RoutedAt.IsZero() {
		return ReviewRoute{}, fmt.Errorf("%w: invalid review route", ErrInvalidEvidence)
	}
	return record, nil
}

func PublishReviewRoute(task Task, record ReviewRoute) error {
	if record.Version != ReviewRecordVersion || record.TaskID != task.ID || record.Attempt < 1 ||
		record.Sequence < 1 || !reviewSessionID.MatchString(record.SessionID) || record.RoutedAt.IsZero() {
		return fmt.Errorf("%w: invalid review route", ErrInvalidEvidence)
	}
	homeDir, err := home()
	if err != nil {
		return err
	}
	return publishImmutable(ReviewRoutePath(homeDir, task.MissionID, task.ID, record.Attempt, record.Sequence), record)
}

func ReadReviewApprovalAcknowledgement(missionID, taskID string, attempt, sequence int) (ReviewApprovalAcknowledgement, error) {
	var record ReviewApprovalAcknowledgement
	homeDir, err := home()
	if err != nil {
		return record, err
	}
	data, err := readReviewBytes(ReviewApprovalAckPath(homeDir, missionID, taskID, attempt, sequence))
	if err != nil {
		return record, err
	}
	if err := decodeStrict(data, &record); err != nil {
		return ReviewApprovalAcknowledgement{}, err
	}
	if record.Version != ReviewRecordVersion || record.TaskID != taskID || record.Attempt != attempt ||
		record.Sequence != sequence || !reviewSessionID.MatchString(record.SessionID) ||
		!reviewSHA.MatchString(record.HeadSHA) || record.SeenAt.IsZero() {
		return ReviewApprovalAcknowledgement{}, fmt.Errorf("%w: invalid review approval acknowledgement", ErrInvalidEvidence)
	}
	return record, nil
}

func PublishReviewApprovalAcknowledgement(task Task, record ReviewApprovalAcknowledgement) error {
	if record.Version != ReviewRecordVersion || record.TaskID != task.ID || record.Attempt < 1 || record.Sequence < 1 ||
		!reviewSessionID.MatchString(record.SessionID) || !reviewSHA.MatchString(record.HeadSHA) || record.SeenAt.IsZero() {
		return fmt.Errorf("%w: invalid review approval acknowledgement", ErrInvalidEvidence)
	}
	homeDir, err := home()
	if err != nil {
		return err
	}
	return publishImmutable(ReviewApprovalAckPath(homeDir, task.MissionID, task.ID, record.Attempt, record.Sequence), record)
}

func reviewSequenceName(name string) (int, bool) {
	if len(name) != 25 || !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 32)
	return int(value), err == nil && value > 0 && fmt.Sprintf("%020d.json", value) == name
}

func publishImmutable(recordPath string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode immutable record: %w", err)
	}
	data = append(data, '\n')
	existing, err := readReviewBytes(recordPath)
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("%w: immutable record already exists with different bytes", ErrInvalidEvidence)
	}
	if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("inspect immutable record: %w", err)
	}
	return PublishBytes(recordPath, data)
}

func regularReviewEntry(entry os.DirEntry) bool {
	info, err := entry.Info()
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func readReviewBytes(recordPath string) ([]byte, error) {
	info, err := os.Lstat(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect review record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: review record is not a regular file", ErrInvalidEvidence)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return nil, fmt.Errorf("read review record: %w", err)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: review record is not UTF-8 JSON", ErrInvalidEvidence)
	}
	return data, nil
}
