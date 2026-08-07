// Package domain contains the durable control-plane value types.
package domain

import (
	"encoding/json"
	"time"
)

type (
	ProjectID  string
	MissionID  string
	TaskID     string
	SessionID  string
	CommandID  string
	ArtifactID string
)

// Project is a registered local repository. Registry mutations are owned by
// the Store; callers use this projection for read-only inspection.
type Project struct {
	ID        ProjectID `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

type TreehouseLeaseState string

const (
	TreehouseLeaseActive   TreehouseLeaseState = "active"
	TreehouseLeaseReleased TreehouseLeaseState = "released"
	TreehouseLeaseFenced   TreehouseLeaseState = "fenced"
	TreehouseLeaseMissing  TreehouseLeaseState = "missing"
)

// TreehouseLease is the durable identity binding one task attempt to one
// Treehouse worktree. ProjectPath is runtime-only routing metadata loaded from
// the registered project; it is intentionally excluded from persisted command
// results and events.
type TreehouseLease struct {
	LeaseID      string              `json:"lease_id"`
	TaskID       TaskID              `json:"task_id"`
	Attempt      int                 `json:"attempt"`
	LeaseHolder  string              `json:"lease_holder"`
	WorktreePath string              `json:"worktree_path"`
	Project      string              `json:"project"`
	ProjectPath  string              `json:"-"`
	Branch       string              `json:"branch"`
	BaseSHA      string              `json:"base_sha"`
	State        TreehouseLeaseState `json:"state"`
	AcquiredAt   time.Time           `json:"acquired_at"`
	ReleasedAt   *time.Time          `json:"released_at,omitempty"`
}

type Criterion struct {
	Description string `json:"description"`
}

type MissionState string

const (
	MissionActive     MissionState = "active"
	MissionCompleting MissionState = "completing"
	MissionCompleted  MissionState = "completed"
	MissionCancelled  MissionState = "cancelled"
)

type MissionBudget struct {
	MaxWallClock       time.Duration `json:"max_wall_clock"`
	MaxConcurrentTasks int           `json:"max_concurrent_tasks"`
	MaxTaskAttempts    int           `json:"max_task_attempts"`
	MaxValidationRuns  int           `json:"max_validation_runs"`
	MaxTokens          *int64        `json:"max_tokens,omitempty"`
	// MaxCost is a base-10 decimal string so persistence never introduces
	// floating-point rounding into a budget.
	MaxCost *string `json:"max_cost,omitempty"`
}

type Mission struct {
	ID                 MissionID     `json:"id"`
	ProjectID          ProjectID     `json:"project_id"`
	CommanderSessionID SessionID     `json:"commander_session_id,omitempty"`
	Title              string        `json:"title"`
	Objective          string        `json:"objective"`
	AcceptanceCriteria []Criterion   `json:"acceptance_criteria"`
	State              MissionState  `json:"state"`
	Version            int64         `json:"version"`
	Budget             MissionBudget `json:"budget"`
	CreatedAt          time.Time     `json:"created_at"`
	CompletedAt        *time.Time    `json:"completed_at,omitempty"`
}

type TaskKind string

const (
	TaskImplementation TaskKind = "implementation"
	TaskScout          TaskKind = "scout"
	TaskReview         TaskKind = "review"
)

type DeliveryMode string

const (
	DeliveryGate   DeliveryMode = "gate"
	DeliveryPR     DeliveryMode = "pr"
	DeliveryBranch DeliveryMode = "branch"
)

type TaskState string

const (
	TaskQueued          TaskState = "queued"
	TaskProvisioning    TaskState = "provisioning"
	TaskStarting        TaskState = "starting"
	TaskRunning         TaskState = "running"
	TaskBlocked         TaskState = "blocked"
	TaskCollecting      TaskState = "collecting"
	TaskReady           TaskState = "ready"
	TaskReportReady     TaskState = "report_ready"
	TaskValidating      TaskState = "validating"
	TaskDeliveryBlocked TaskState = "delivery_blocked"
	TaskDelivered       TaskState = "delivered"
	TaskDeliveredBranch TaskState = "delivered_branch"
	TaskNeedsAttention  TaskState = "needs_attention"
	TaskCancelling      TaskState = "cancelling"
	TaskCancelled       TaskState = "cancelled"
	TaskFailed          TaskState = "failed"
)

type Task struct {
	ID                 TaskID       `json:"id"`
	MissionID          MissionID    `json:"mission_id"`
	ParentTaskID       *TaskID      `json:"parent_task_id,omitempty"`
	BaseTaskID         *TaskID      `json:"base_task_id,omitempty"`
	BaseSHA            string       `json:"base_sha,omitempty"`
	Kind               TaskKind     `json:"kind"`
	Title              string       `json:"title"`
	Objective          string       `json:"objective"`
	AcceptanceCriteria []Criterion  `json:"acceptance_criteria"`
	State              TaskState    `json:"state"`
	Version            int64        `json:"version"`
	Priority           int          `json:"priority"`
	WorkerAgent        string       `json:"worker_agent,omitempty"`
	DeliveryMode       DeliveryMode `json:"delivery_mode"`
	CurrentAttempt     int          `json:"current_attempt"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
	CompletedAt        *time.Time   `json:"completed_at,omitempty"`
}

type WorkerSessionState string

const (
	WorkerSessionStarting WorkerSessionState = "starting"
	WorkerSessionRunning  WorkerSessionState = "running"
	WorkerSessionIdle     WorkerSessionState = "idle"
	WorkerSessionInactive WorkerSessionState = "inactive"
	WorkerSessionLost     WorkerSessionState = "lost"
	WorkerSessionFailed   WorkerSessionState = "failed"
	WorkerSessionStopping WorkerSessionState = "stopping"
	WorkerSessionStopped  WorkerSessionState = "stopped"
)

// WorkerSession records durable Herdr placement. PaneID is the operational
// identity; workspace and tab IDs are retained only for observation and UI.
type WorkerSession struct {
	ID               SessionID          `json:"id"`
	TaskID           TaskID             `json:"task_id"`
	Attempt          int                `json:"attempt"`
	Runtime          string             `json:"runtime"`
	State            WorkerSessionState `json:"state"`
	Version          int64              `json:"version"`
	HerdrSessionName string             `json:"herdr_session_name"`
	HerdrWorkspaceID string             `json:"herdr_workspace_id"`
	HerdrTabID       string             `json:"herdr_tab_id"`
	HerdrPaneID      string             `json:"herdr_pane_id"`
	HerdrAgentName   string             `json:"herdr_agent_name"`
	AgentSessionID   string             `json:"agent_session_id"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	LastObservedAt   *time.Time         `json:"last_observed_at,omitempty"`
	IdleAt           *time.Time         `json:"idle_at,omitempty"`
	InactiveAt       *time.Time         `json:"inactive_at,omitempty"`
	RecoveryPromptAt *time.Time         `json:"recovery_prompt_at,omitempty"`
	StoppedAt        *time.Time         `json:"stopped_at,omitempty"`
	FailureReason    string             `json:"failure_reason,omitempty"`
	Budget           WorkerBudget       `json:"budget"`
	RestartCount     int                `json:"restart_count"`
	FixRoundCount    int                `json:"fix_round_count"`
}

type WorkerBudget struct {
	MaxRuntime   time.Duration `json:"max_runtime"`
	MaxRestarts  int           `json:"max_restarts"`
	MaxFixRounds int           `json:"max_fix_rounds"`
}

type CommanderSessionState string

const (
	CommanderSessionStarting       CommanderSessionState = "starting"
	CommanderSessionRunning        CommanderSessionState = "running"
	CommanderSessionIdle           CommanderSessionState = "idle"
	CommanderSessionNeedsAttention CommanderSessionState = "needs_attention"
	CommanderSessionFailed         CommanderSessionState = "failed"
	CommanderSessionStopping       CommanderSessionState = "stopping"
	CommanderSessionStopped        CommanderSessionState = "stopped"
)

// CommanderSession is the durable binding between one project and one
// resumable interactive agent. MissionID is empty during conversational
// intake and is bound when that commander creates the mission. Herdr pane
// identity is operational placement; AgentSessionID is the logical runtime
// identity used after a daemon restart.
type CommanderSession struct {
	ID                SessionID             `json:"id"`
	MissionID         MissionID             `json:"mission_id"`
	ProjectID         ProjectID             `json:"project_id"`
	Runtime           string                `json:"runtime"`
	State             CommanderSessionState `json:"state"`
	Version           int64                 `json:"version"`
	HerdrSessionName  string                `json:"herdr_session_name"`
	HerdrWorkspaceID  string                `json:"herdr_workspace_id"`
	HerdrTabID        string                `json:"herdr_tab_id"`
	HerdrPaneID       string                `json:"herdr_pane_id"`
	HerdrAgentName    string                `json:"herdr_agent_name"`
	AgentSessionID    string                `json:"agent_session_id"`
	Model             string                `json:"model,omitempty"`
	PiExtensionPath   string                `json:"pi_extension_path,omitempty"`
	LastEventSequence int64                 `json:"last_event_sequence"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
	LastObservedAt    *time.Time            `json:"last_observed_at,omitempty"`
	StoppedAt         *time.Time            `json:"stopped_at,omitempty"`
	FailureReason     string                `json:"failure_reason,omitempty"`
	Budget            CommanderBudget       `json:"budget"`
	TurnCount         int                   `json:"turn_count"`
}

type CommanderBudget struct {
	MaxTurns    int           `json:"max_turns"`
	MaxDuration time.Duration `json:"max_duration"`
}

type VerificationResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
}

type WorkerResult struct {
	Version      int                  `json:"version"`
	Status       string               `json:"status"`
	Summary      string               `json:"summary"`
	Verification []VerificationResult `json:"verification"`
	ChangedFiles []string             `json:"changed_files"`
	Risks        []string             `json:"risks"`
}

type TaskAttempt struct {
	TaskID               TaskID        `json:"task_id"`
	Attempt              int           `json:"attempt"`
	BaseSHA              string        `json:"base_sha,omitempty"`
	HeadSHA              string        `json:"head_sha,omitempty"`
	Branch               string        `json:"branch,omitempty"`
	WorktreePath         string        `json:"worktree_path,omitempty"`
	TreehouseLeaseID     string        `json:"treehouse_lease_id,omitempty"`
	TreehouseLeaseHolder string        `json:"treehouse_lease_holder,omitempty"`
	WorkerSessionID      SessionID     `json:"worker_session_id,omitempty"`
	ResultPath           string        `json:"result_path,omitempty"`
	ResultSHA256         string        `json:"result_sha256,omitempty"`
	Result               *WorkerResult `json:"result,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
	StartedAt            *time.Time    `json:"started_at,omitempty"`
	CompletedAt          *time.Time    `json:"completed_at,omitempty"`
}

type Event struct {
	Sequence  int64           `json:"sequence"`
	MissionID *MissionID      `json:"mission_id,omitempty"`
	TaskID    *TaskID         `json:"task_id,omitempty"`
	Actor     string          `json:"actor"`
	Type      string          `json:"type"`
	CommandID *CommandID      `json:"command_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type Command struct {
	ID          CommandID       `json:"id"`
	Kind        string          `json:"kind"`
	RequestHash string          `json:"request_hash"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}
