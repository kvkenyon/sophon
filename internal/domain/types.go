// Package domain contains the durable control-plane value types.
package domain

import (
	"encoding/json"
	"time"
)

type (
	ProjectID string
	MissionID string
	TaskID    string
	SessionID string
	CommandID string
)

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
	ID             TaskID       `json:"id"`
	MissionID      MissionID    `json:"mission_id"`
	ParentTaskID   *TaskID      `json:"parent_task_id,omitempty"`
	BaseTaskID     *TaskID      `json:"base_task_id,omitempty"`
	BaseSHA        string       `json:"base_sha,omitempty"`
	Kind           TaskKind     `json:"kind"`
	Title          string       `json:"title"`
	Objective      string       `json:"objective"`
	State          TaskState    `json:"state"`
	Version        int64        `json:"version"`
	Priority       int          `json:"priority"`
	WorkerAgent    string       `json:"worker_agent,omitempty"`
	DeliveryMode   DeliveryMode `json:"delivery_mode"`
	CurrentAttempt int          `json:"current_attempt"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	CompletedAt    *time.Time   `json:"completed_at,omitempty"`
}

type TaskAttempt struct {
	TaskID               TaskID     `json:"task_id"`
	Attempt              int        `json:"attempt"`
	BaseSHA              string     `json:"base_sha,omitempty"`
	HeadSHA              string     `json:"head_sha,omitempty"`
	Branch               string     `json:"branch,omitempty"`
	WorktreePath         string     `json:"worktree_path,omitempty"`
	TreehouseLeaseID     string     `json:"treehouse_lease_id,omitempty"`
	TreehouseLeaseHolder string     `json:"treehouse_lease_holder,omitempty"`
	WorkerSessionID      SessionID  `json:"worker_session_id,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
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
