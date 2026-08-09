// Package store implements Sophon's filesystem state model: canonical typed
// records under the data home, atomically published, with status derived at
// read time. There is no database, ledger, or projection.
//
// Not-found semantics: every Read* helper returns an error wrapping
// ErrNotFound when the record does not exist; callers use errors.Is.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	"sophon/internal/herdr"
)

var (
	// ErrNotFound marks a missing record. Wrap it; never compare by string.
	ErrNotFound = errors.New("store record not found")
	// ErrInvalidEvidence marks a malformed canonical worker completion or
	// non-completion record. Status surfaces it instead of treating presence as
	// truth.
	ErrInvalidEvidence = errors.New("invalid worker evidence")
)

const (
	CompletionSubmissionName  = "completion-submission.json"
	ReportSubmissionName      = "report-submission.json"
	WorkerReportScopeMismatch = "scope-mismatch"
	WorkerReportBlocked       = "blocked"
)

// WorkerReport is canonical typed non-completion evidence. It preserves the
// exact task/attempt/head identity plus work and verification disclosure
// without claiming completion, verification, or delivery readiness.
type WorkerReport struct {
	Version      int                         `json:"version"`
	Status       string                      `json:"status"`
	TaskID       string                      `json:"task_id"`
	Attempt      int                         `json:"attempt"`
	HeadSHA      string                      `json:"head_sha"`
	Reason       string                      `json:"reason"`
	Verification []domain.VerificationResult `json:"verification"`
	Evidence     []string                    `json:"evidence"`
	ChangedFiles []string                    `json:"changed_files"`
	DirtyWork    bool                        `json:"dirty_work"`
	Risks        []string                    `json:"risks"`
}

// Mission is durable mission intent.
type Mission struct {
	ID          string    `json:"id"`
	ProjectPath string    `json:"project_path"`
	Title       string    `json:"title"`
	Objective   string    `json:"objective"`
	CreatedAt   time.Time `json:"created_at"`
}

// Task is durable task intent plus the current-attempt incarnation token.
type Task struct {
	ID                string              `json:"id"`
	MissionID         string              `json:"mission_id"`
	Title             string              `json:"title"`
	Kind              domain.TaskKind     `json:"kind"`
	DeliveryMode      domain.DeliveryMode `json:"delivery_mode"`
	ValidationCommand string              `json:"validation_command,omitempty"`
	CurrentAttempt    int                 `json:"current_attempt"`
	CreatedAt         time.Time           `json:"created_at"`
}

// Spawn is the spawn receipt for one attempt, written only after every
// external effect (lease, branch, pane) has succeeded.
type Spawn struct {
	TaskID       string        `json:"task_id"`
	MissionID    string        `json:"mission_id"`
	Attempt      int           `json:"attempt"`
	WorktreePath string        `json:"worktree_path"`
	Branch       string        `json:"branch"`
	BaseSHA      string        `json:"base_sha"`
	LeaseID      string        `json:"lease_id"`
	LeaseHolder  string        `json:"lease_holder"`
	Pane         herdr.Session `json:"pane"`
	AgentRuntime string        `json:"agent_runtime"`
	Model        string        `json:"model,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
}

// Outcome is the verified-completion receipt for one attempt.
type Outcome struct {
	TaskID       string    `json:"task_id"`
	Attempt      int       `json:"attempt"`
	HeadSHA      string    `json:"head_sha"`
	Branch       string    `json:"branch"`
	ResultSHA256 string    `json:"result_sha256"`
	VerifiedAt   time.Time `json:"verified_at"`
}

// Validation is one validation receipt pinned to an exact head SHA.
type Validation struct {
	TaskID   string    `json:"task_id"`
	Attempt  int       `json:"attempt"`
	Command  string    `json:"command"`
	HeadSHA  string    `json:"head_sha"`
	ExitCode int       `json:"exit_code"`
	Passed   bool      `json:"passed"`
	RanAt    time.Time `json:"ran_at"`
}

// DeliveryState is the delivery lifecycle: pending intent before the external
// effect, then a terminal receipt.
type DeliveryState string

const (
	DeliveryPending         DeliveryState = "pending"
	DeliveryDeliveredBranch DeliveryState = "delivered_branch"
	DeliveryDeliveredPR     DeliveryState = "delivered_pr"
)

// Terminal reports whether the delivery state is a receipt, not an intent.
func (s DeliveryState) Terminal() bool {
	return s == DeliveryDeliveredBranch || s == DeliveryDeliveredPR
}

// Delivery is the delivery intent-plus-receipt record for one attempt.
type Delivery struct {
	TaskID      string              `json:"task_id"`
	Attempt     int                 `json:"attempt"`
	Mode        domain.DeliveryMode `json:"mode"`
	Repository  string              `json:"repository,omitempty"`
	Branch      string              `json:"branch"`
	HeadSHA     string              `json:"head_sha"`
	State       DeliveryState       `json:"state"`
	PRURL       string              `json:"pr_url,omitempty"`
	PRNumber    int                 `json:"pr_number,omitempty"`
	IntentAt    time.Time           `json:"intent_at"`
	DeliveredAt *time.Time          `json:"delivered_at,omitempty"`
}

// Release is the conditional lease-release receipt for one attempt.
type Release struct {
	TaskID      string    `json:"task_id"`
	Attempt     int       `json:"attempt"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	ReleasedAt  time.Time `json:"released_at"`
}

// CommanderRegistration is the volatile wake and placement address of the
// currently attached commander: its exact Herdr session, workspace, tab, and
// pane. It is liveness and presentation routing only — never task truth,
// never canonical state, and never ownership of the commander. A fresh
// attach atomically replaces it; nothing reads it to derive status.
type CommanderRegistration struct {
	Session     string    `json:"session"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	TabID       string    `json:"tab_id,omitempty"`
	PaneID      string    `json:"pane_id"`
	Runtime     string    `json:"runtime,omitempty"`
	AttachedAt  time.Time `json:"attached_at"`
}

// home resolves the data home; SOPHON_DATA_HOME wins so tests are hermetic.
func home() (string, error) {
	return datahome.Dir()
}

// MissionsRoot is the directory containing all missions.
func MissionsRoot(home string) string { return filepath.Join(home, "missions") }

// MissionDir is one mission's directory.
func MissionDir(home, missionID string) string {
	return filepath.Join(MissionsRoot(home), missionID)
}

// MissionPath is one mission's intent record.
func MissionPath(home, missionID string) string {
	return filepath.Join(MissionDir(home, missionID), "mission.json")
}

// TaskDir is one task's directory under its mission.
func TaskDir(home, missionID, taskID string) string {
	return filepath.Join(MissionDir(home, missionID), "tasks", taskID)
}

// TaskPath is one task's intent record with the current-attempt token.
func TaskPath(home, missionID, taskID string) string {
	return filepath.Join(TaskDir(home, missionID, taskID), "task.json")
}

// AttemptDir is one attempt's record directory.
func AttemptDir(home, missionID, taskID string, attempt int) string {
	return filepath.Join(TaskDir(home, missionID, taskID), "attempts", strconv.Itoa(attempt))
}

// AttemptPath names one typed record inside an attempt directory.
func AttemptPath(home, missionID, taskID string, attempt int, name string) string {
	return filepath.Join(AttemptDir(home, missionID, taskID, attempt), name)
}

// StateDir holds volatile wake lines and the shared-mutation lock.
func StateDir(home string) string { return filepath.Join(home, "state") }

// LockDir is the shared-mutation lock directory.
func LockDir(home string) string { return filepath.Join(StateDir(home), ".lock") }

// WakePath is one task's volatile wake-line file; never truth.
func WakePath(home, taskID string) string {
	return filepath.Join(StateDir(home), taskID+".status")
}

// CommanderPath is the volatile commander registration file; liveness and
// presentation routing only, never truth.
func CommanderPath(home string) string {
	return filepath.Join(StateDir(home), "commander.json")
}

// WorkerSkillDir is the per-attempt materialized skill directory.
func WorkerSkillDir(home, taskID string, attempt int) string {
	return filepath.Join(home, "skills", "worker", taskID, strconv.Itoa(attempt))
}

// Publish atomically writes v as indented JSON with mode 0600: temp file in
// the same directory, fsync, rename, fsync the directory. Readers never see
// torn records; a same-path publish atomically replaces the previous record.
func Publish(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode record %s: %w", path, err)
	}
	return PublishBytes(path, append(data, '\n'))
}

// PublishBytes is the raw-bytes variant of Publish, used for brief.md and
// validated canonical evidence whose submitted bytes must survive verbatim.
func PublishBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create record directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".publish-*")
	if err != nil {
		return fmt.Errorf("create temporary record: %w", err)
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary record: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary record: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("publish record: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open record directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync record directory: %w", err)
	}
	return nil
}

// read decodes one record, mapping absence to ErrNotFound.
func read(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return fmt.Errorf("read record %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode record %s: %w", path, err)
	}
	return nil
}

// exists reports whether a record is present; only genuine absence is false.
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect record %s: %w", path, err)
}

// ReadMission loads one mission's intent record.
func ReadMission(missionID string) (Mission, error) {
	var mission Mission
	homeDir, err := home()
	if err != nil {
		return mission, err
	}
	return mission, read(MissionPath(homeDir, missionID), &mission)
}

// ReadTask loads one task's intent record under a known mission.
func ReadTask(missionID, taskID string) (Task, error) {
	var task Task
	homeDir, err := home()
	if err != nil {
		return task, err
	}
	return task, read(TaskPath(homeDir, missionID, taskID), &task)
}

// FindTask locates a task by ID across all missions. Task IDs are random and
// unique, so the first match is the only match.
func FindTask(taskID string) (Task, error) {
	missions, err := ListMissions()
	if err != nil {
		return Task{}, err
	}
	for _, mission := range missions {
		task, err := ReadTask(mission.ID, taskID)
		if err == nil {
			return task, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Task{}, err
		}
	}
	return Task{}, fmt.Errorf("%w: task %s", ErrNotFound, taskID)
}

// ReadSpawn loads one attempt's spawn receipt.
func ReadSpawn(missionID, taskID string, attempt int) (Spawn, error) {
	var spawn Spawn
	homeDir, err := home()
	if err != nil {
		return spawn, err
	}
	return spawn, read(AttemptPath(homeDir, missionID, taskID, attempt, "spawn.json"), &spawn)
}

// ReadResult strictly decodes one canonical completion record.
func ReadResult(missionID, taskID string, attempt int) (domain.WorkerResult, error) {
	homeDir, err := home()
	if err != nil {
		return domain.WorkerResult{}, err
	}
	data, err := readBytes(AttemptPath(homeDir, missionID, taskID, attempt, "result.json"))
	if err != nil {
		return domain.WorkerResult{}, err
	}
	return DecodeWorkerResult(data)
}

// ReadReport strictly decodes one canonical non-completion record.
func ReadReport(missionID, taskID string, attempt int) (WorkerReport, error) {
	homeDir, err := home()
	if err != nil {
		return WorkerReport{}, err
	}
	data, err := readBytes(AttemptPath(homeDir, missionID, taskID, attempt, "report.json"))
	if err != nil {
		return WorkerReport{}, err
	}
	return DecodeWorkerReport(data)
}

func readBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read record %s: %w", path, err)
	}
	return data, nil
}

// DecodeWorkerResult is the single strict schema boundary for canonical
// completion evidence and its submission form.
func DecodeWorkerResult(data []byte) (domain.WorkerResult, error) {
	var result domain.WorkerResult
	if err := decodeStrict(data, &result); err != nil {
		return result, err
	}
	if result.Version != 1 || result.Status != "completed" || strings.TrimSpace(result.Summary) == "" ||
		len(result.Verification) == 0 || len(result.ChangedFiles) == 0 || result.Risks == nil {
		return result, fmt.Errorf("%w: version, completed status, summary, verification, changed_files, and risks are required", ErrInvalidEvidence)
	}
	for _, check := range result.Verification {
		if strings.TrimSpace(check.Command) == "" || check.ExitCode != 0 {
			return result, fmt.Errorf("%w: verification entries require a command and zero exit code", ErrInvalidEvidence)
		}
	}
	if err := validateChangedFiles(result.ChangedFiles); err != nil {
		return result, err
	}
	return result, nil
}

// DecodeWorkerReport is the single strict schema boundary for canonical typed
// non-completion evidence and its submission form.
func DecodeWorkerReport(data []byte) (WorkerReport, error) {
	var raw struct {
		Version      int                         `json:"version"`
		Status       string                      `json:"status"`
		TaskID       string                      `json:"task_id"`
		Attempt      int                         `json:"attempt"`
		HeadSHA      string                      `json:"head_sha"`
		Reason       string                      `json:"reason"`
		Verification []domain.VerificationResult `json:"verification"`
		Evidence     []string                    `json:"evidence"`
		ChangedFiles []string                    `json:"changed_files"`
		DirtyWork    *bool                       `json:"dirty_work"`
		Risks        []string                    `json:"risks"`
	}
	if err := decodeStrict(data, &raw); err != nil {
		return WorkerReport{}, err
	}
	validStatus := raw.Status == WorkerReportScopeMismatch || raw.Status == WorkerReportBlocked
	if raw.Version != 1 || !validStatus || strings.TrimSpace(raw.TaskID) == "" || raw.Attempt < 1 ||
		strings.TrimSpace(raw.HeadSHA) == "" || strings.TrimSpace(raw.Reason) == "" || raw.DirtyWork == nil ||
		raw.Verification == nil || raw.Evidence == nil || raw.ChangedFiles == nil || raw.Risks == nil ||
		(len(raw.Verification) == 0 && len(raw.Evidence) == 0) {
		return WorkerReport{}, fmt.Errorf("%w: version, typed status, task_id, attempt, head_sha, reason, verification/evidence, changed_files, dirty_work, and risks are required", ErrInvalidEvidence)
	}
	for _, check := range raw.Verification {
		if strings.TrimSpace(check.Command) == "" {
			return WorkerReport{}, fmt.Errorf("%w: report verification entries require a command", ErrInvalidEvidence)
		}
	}
	for _, evidence := range raw.Evidence {
		if strings.TrimSpace(evidence) == "" {
			return WorkerReport{}, fmt.Errorf("%w: report evidence entries cannot be empty", ErrInvalidEvidence)
		}
	}
	if err := validateChangedFiles(raw.ChangedFiles); err != nil {
		return WorkerReport{}, err
	}
	return WorkerReport{Version: raw.Version, Status: raw.Status, TaskID: raw.TaskID, Attempt: raw.Attempt,
		HeadSHA: raw.HeadSHA, Reason: raw.Reason, Verification: raw.Verification, Evidence: raw.Evidence,
		ChangedFiles: raw.ChangedFiles, DirtyWork: *raw.DirtyWork, Risks: raw.Risks}, nil
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: decode JSON: %v", ErrInvalidEvidence, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidEvidence)
		}
		return fmt.Errorf("%w: trailing content: %v", ErrInvalidEvidence, err)
	}
	return nil
}

func validateChangedFiles(files []string) error {
	seen := make(map[string]struct{}, len(files))
	for _, changed := range files {
		clean := filepath.Clean(changed)
		if changed == "" || filepath.IsAbs(changed) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean != changed {
			return fmt.Errorf("%w: changed_files contains unsafe path %q", ErrInvalidEvidence, changed)
		}
		if _, exists := seen[changed]; exists {
			return fmt.Errorf("%w: duplicate changed file %q", ErrInvalidEvidence, changed)
		}
		seen[changed] = struct{}{}
	}
	return nil
}

// ReadOutcome loads one attempt's verified-completion receipt.
func ReadOutcome(missionID, taskID string, attempt int) (Outcome, error) {
	var outcome Outcome
	homeDir, err := home()
	if err != nil {
		return outcome, err
	}
	return outcome, read(AttemptPath(homeDir, missionID, taskID, attempt, "outcome.json"), &outcome)
}

// ReadValidation loads one attempt's validation receipt.
func ReadValidation(missionID, taskID string, attempt int) (Validation, error) {
	var validation Validation
	homeDir, err := home()
	if err != nil {
		return validation, err
	}
	return validation, read(AttemptPath(homeDir, missionID, taskID, attempt, "validation.json"), &validation)
}

// ReadDelivery loads one attempt's delivery intent or receipt.
func ReadDelivery(missionID, taskID string, attempt int) (Delivery, error) {
	var delivery Delivery
	homeDir, err := home()
	if err != nil {
		return delivery, err
	}
	return delivery, read(AttemptPath(homeDir, missionID, taskID, attempt, "delivery.json"), &delivery)
}

// ReadRelease loads one attempt's lease-release receipt.
func ReadRelease(missionID, taskID string, attempt int) (Release, error) {
	var release Release
	homeDir, err := home()
	if err != nil {
		return release, err
	}
	data, err := readBytes(AttemptPath(homeDir, missionID, taskID, attempt, "release.json"))
	if err != nil {
		return release, err
	}
	if err := decodeStrict(data, &release); err != nil {
		return release, err
	}
	return release, nil
}

// ReadCommander loads the volatile commander registration, mapping absence to
// ErrNotFound like every other record. Callers must treat the value as a
// best-effort notification address, never as state.
func ReadCommander() (CommanderRegistration, error) {
	var registration CommanderRegistration
	homeDir, err := home()
	if err != nil {
		return registration, err
	}
	return registration, read(CommanderPath(homeDir), &registration)
}

// PublishCommander atomically replaces the volatile commander registration.
// The caller holds the shared-mutation lock. Replacement is the only
// transition: no recovery, no task-truth mutation.
func PublishCommander(registration CommanderRegistration) error {
	homeDir, err := home()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir(homeDir), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	return Publish(CommanderPath(homeDir), registration)
}

// CreateMission publishes a new mission's intent record.
func CreateMission(mission Mission) error {
	homeDir, err := home()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(MissionDir(homeDir, mission.ID), "tasks"), 0o700); err != nil {
		return fmt.Errorf("create mission directory: %w", err)
	}
	return Publish(MissionPath(homeDir, mission.ID), mission)
}

// CreateTask publishes a new task's intent record under its mission.
func CreateTask(task Task) error {
	homeDir, err := home()
	if err != nil {
		return err
	}
	if _, err := ReadMission(task.MissionID); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(TaskDir(homeDir, task.MissionID, task.ID), "attempts"), 0o700); err != nil {
		return fmt.Errorf("create task directory: %w", err)
	}
	return Publish(TaskPath(homeDir, task.MissionID, task.ID), task)
}

// ListMissions returns every mission, sorted by ID for stable output.
func ListMissions() ([]Mission, error) {
	homeDir, err := home()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(MissionsRoot(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list missions: %w", err)
	}
	var missions []Mission
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var mission Mission
		if err := read(MissionPath(homeDir, entry.Name()), &mission); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		missions = append(missions, mission)
	}
	sort.Slice(missions, func(i, j int) bool { return missions[i].ID < missions[j].ID })
	return missions, nil
}

// ListTasks returns every task under one mission, sorted by ID.
func ListTasks(missionID string) ([]Task, error) {
	homeDir, err := home()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(MissionDir(homeDir, missionID), "tasks"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	var tasks []Task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var task Task
		if err := read(TaskPath(homeDir, missionID, entry.Name()), &task); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// BumpAttempt increments the task's current-attempt token and republishes the
// intent record. The caller must hold the shared-mutation lock.
func BumpAttempt(missionID, taskID string) (Task, error) {
	task, err := ReadTask(missionID, taskID)
	if err != nil {
		return Task{}, err
	}
	task.CurrentAttempt++
	homeDir, err := home()
	if err != nil {
		return Task{}, err
	}
	return task, Publish(TaskPath(homeDir, missionID, taskID), task)
}

// Derived task states. Anything beyond active is terminal for the store
// layer; active is augmented by the caller with live pane observation.
const (
	StateQueued          = "queued"
	StateDelivered       = "delivered"
	StateVerified        = "verified"
	StateReady           = "ready"
	StateAttention       = "attention"
	StateInvalidEvidence = "invalid-evidence"
	StateReleased        = "released"
	StateActive          = "active"
)

// TaskStatus is the read-time derivation of one task's lifecycle from its
// canonical records alone.
type TaskStatus struct {
	Task    Task   `json:"task"`
	Attempt int    `json:"attempt"`
	State   string `json:"state"`
	Detail  string `json:"detail,omitempty"`
	// DeliveryState preserves whether released historical work had previously
	// been delivered. Release itself never implies delivery.
	DeliveryState string `json:"delivery_state,omitempty"`
}

// Derive computes one task from strict current-attempt records. Exact release
// derives released; typed report derives attention; malformed or conflicting
// evidence derives invalid-evidence; then delivery, outcome, and schema-valid
// completion derive delivered, verified, and ready respectively. Otherwise the
// task is active. Fenced attempts and wake lines never influence the result.
func Derive(task Task) (TaskStatus, error) {
	status := TaskStatus{Task: task, Attempt: task.CurrentAttempt, State: StateQueued}
	if task.CurrentAttempt < 1 {
		return status, nil
	}
	homeDir, err := home()
	if err != nil {
		return status, err
	}
	status.State = StateActive
	record := func(name string) (bool, error) {
		return exists(AttemptPath(homeDir, task.MissionID, task.ID, task.CurrentAttempt, name))
	}
	if present, err := record("release.json"); err != nil {
		return status, err
	} else if present {
		released, err := ReadRelease(task.MissionID, task.ID, task.CurrentAttempt)
		if err != nil {
			status.State = StateInvalidEvidence
			status.Detail = "invalid current-attempt release: " + err.Error()
			return status, nil
		}
		spawn, err := ReadSpawn(task.MissionID, task.ID, task.CurrentAttempt)
		if err != nil || released.TaskID != task.ID || released.Attempt != task.CurrentAttempt ||
			released.LeaseID != spawn.LeaseID || released.LeaseHolder != spawn.LeaseHolder || released.ReleasedAt.IsZero() {
			status.State = StateInvalidEvidence
			status.Detail = "current-attempt release identity does not match spawn receipt"
			return status, nil
		}
		status.State = StateReleased
		status.DeliveryState = "not-delivered"
		if delivery, deliveryErr := ReadDelivery(task.MissionID, task.ID, task.CurrentAttempt); deliveryErr == nil && delivery.State.Terminal() {
			status.DeliveryState = string(delivery.State)
		} else if deliveryErr != nil && !errors.Is(deliveryErr, ErrNotFound) {
			status.State = StateInvalidEvidence
			status.Detail = "cannot inspect delivery history: " + deliveryErr.Error()
			status.DeliveryState = ""
			return status, nil
		}
		status.Detail = "lease returned; delivery=" + status.DeliveryState
		return status, nil
	}
	resultPresent, err := record("result.json")
	if err != nil {
		return status, err
	}
	reportPresent, err := record("report.json")
	if err != nil {
		return status, err
	}
	if resultPresent && reportPresent {
		status.State = StateInvalidEvidence
		status.Detail = "conflicting result.json and report.json require reconciliation"
		return status, nil
	}
	if reportPresent {
		report, err := ReadReport(task.MissionID, task.ID, task.CurrentAttempt)
		if err != nil {
			status.State = StateInvalidEvidence
			status.Detail = err.Error()
			return status, nil
		}
		if report.TaskID != task.ID || report.Attempt != task.CurrentAttempt {
			status.State = StateInvalidEvidence
			status.Detail = "report identity does not match its canonical attempt path"
			return status, nil
		}
		status.State = StateAttention
		status.Detail = report.Status + ": " + report.Reason
		return status, nil
	}
	if resultPresent {
		if _, err := ReadResult(task.MissionID, task.ID, task.CurrentAttempt); err != nil {
			status.State = StateInvalidEvidence
			status.Detail = err.Error()
			return status, nil
		}
	}
	if present, err := record("delivery.json"); err != nil {
		return status, err
	} else if present {
		delivery, err := ReadDelivery(task.MissionID, task.ID, task.CurrentAttempt)
		if err != nil {
			return status, err
		}
		if delivery.State.Terminal() {
			status.State = StateDelivered
			status.Detail = string(delivery.State)
			return status, nil
		}
	}
	if present, err := record("outcome.json"); err != nil {
		return status, err
	} else if present {
		status.State = StateVerified
		return status, nil
	}
	if resultPresent {
		status.State = StateReady
		status.Detail = "pending verification"
		return status, nil
	}
	return status, nil
}

// AppendWake adds one notification line to the task's volatile wake file.
// Wake lines are best-effort notifications only; nothing may read them for
// truth.
func AppendWake(taskID, line string) error {
	homeDir, err := home()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir(homeDir), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.OpenFile(WakePath(homeDir, taskID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open wake file: %w", err)
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line); err != nil {
		return fmt.Errorf("append wake line: %w", err)
	}
	return nil
}
