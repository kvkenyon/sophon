// Package delivery coordinates immutable-SHA task delivery and recovery.
package delivery

import (
	"context"
	"errors"
	"time"

	"parallel-intellect/internal/domain"
)

type State string

const (
	StatePending         State = "pending"
	StateBlocked         State = "blocked"
	StateDeliveredBranch State = "delivered_branch"
	StateDelivered       State = "delivered"
)

type GateState string

const (
	GateNotRequired GateState = "not_required"
	GatePending     GateState = "pending"
	GatePassed      GateState = "passed"
	GateFailed      GateState = "failed"
)

var (
	ErrHeadMismatch   = errors.New("delivery head does not match the verified attempt head")
	ErrBranchMismatch = errors.New("delivery branch does not match the task attempt branch")
	ErrGateFailed     = errors.New("no-mistakes delivery gate failed")
)

type Record struct {
	TaskID      domain.TaskID       `json:"task_id"`
	Attempt     int                 `json:"attempt"`
	Mode        domain.DeliveryMode `json:"mode"`
	Repository  string              `json:"repository,omitempty"`
	Branch      string              `json:"branch"`
	HeadSHA     string              `json:"head_sha"`
	PRURL       string              `json:"pr_url,omitempty"`
	PRNumber    int                 `json:"pr_number,omitempty"`
	State       State               `json:"state"`
	GateState   GateState           `json:"gate_state"`
	GateOutput  string              `json:"gate_output,omitempty"`
	CommandID   domain.CommandID    `json:"command_id"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	DeliveredAt *time.Time          `json:"delivered_at,omitempty"`
}

type Result struct {
	Task     domain.Task `json:"task"`
	Delivery Record      `json:"delivery"`
}

type Target struct {
	Task        domain.Task
	Attempt     domain.TaskAttempt
	ProjectPath string
}

type Reservation struct {
	TaskID  domain.TaskID `json:"task_id"`
	Attempt int           `json:"attempt"`
	Base    string        `json:"base,omitempty"`
}

type ReserveInput struct {
	TaskID    domain.TaskID `json:"task_id"`
	Operation string        `json:"operation"`
	Base      string        `json:"base,omitempty"`
	Actor     string        `json:"actor"`
}

type PrepareInput struct {
	TaskID           domain.TaskID       `json:"task_id"`
	Attempt          int                 `json:"attempt"`
	ExpectedVersion  int64               `json:"expected_version"`
	Mode             domain.DeliveryMode `json:"mode"`
	Repository       string              `json:"repository,omitempty"`
	Branch           string              `json:"branch"`
	HeadSHA          string              `json:"head_sha"`
	RequestCommandID domain.CommandID    `json:"request_command_id"`
	Actor            string              `json:"actor"`
}

type GateInput struct {
	TaskID  domain.TaskID `json:"task_id"`
	Attempt int           `json:"attempt"`
	HeadSHA string        `json:"head_sha"`
	Passed  bool          `json:"passed"`
	Output  string        `json:"output,omitempty"`
	Actor   string        `json:"actor"`
}

type CompleteInput struct {
	TaskID     domain.TaskID `json:"task_id"`
	Attempt    int           `json:"attempt"`
	Repository string        `json:"repository"`
	Branch     string        `json:"branch"`
	HeadSHA    string        `json:"head_sha"`
	PRURL      string        `json:"pr_url"`
	PRNumber   int           `json:"pr_number"`
	Actor      string        `json:"actor"`
}

type Store interface {
	ReserveDelivery(context.Context, domain.CommandID, ReserveInput) (Reservation, error)
	DeliveryTarget(context.Context, domain.TaskID, int) (Target, error)
	Delivery(context.Context, domain.TaskID, int) (*Record, error)
	PrepareDelivery(context.Context, domain.CommandID, PrepareInput) (Result, error)
	RecordDeliveryGate(context.Context, domain.CommandID, GateInput) (Result, error)
	CompleteDelivery(context.Context, domain.CommandID, CompleteInput) (Result, error)
	TreehouseLease(context.Context, domain.TaskID, int) (domain.TreehouseLease, error)
}

type LocalGit interface {
	VerifyHead(context.Context, string, string, string) error
	Repository(context.Context, string) (string, error)
}

type PullRequest struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	HeadSHA    string `json:"head_sha"`
	URL        string `json:"url"`
	Number     int    `json:"number"`
}

type PullRequestInput struct {
	Repository string
	Worktree   string
	Branch     string
	HeadSHA    string
	Base       string
	Title      string
	Body       string
}

type Remote interface {
	Push(context.Context, string, string, string, string) error
	FindPullRequest(context.Context, string, string, string, string) (*PullRequest, error)
	CreatePullRequest(context.Context, PullRequestInput) (PullRequest, error)
	HeadSHA(context.Context, string, string, string) (string, error)
}

type GateResult struct {
	Passed bool
	Output string
}

type Gate interface {
	Run(context.Context, string, string) (GateResult, error)
}

type LeaseReleaser interface {
	Release(context.Context, domain.CommandID, domain.TaskID, int) (domain.TreehouseLease, error)
}
