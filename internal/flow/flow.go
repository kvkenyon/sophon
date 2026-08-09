// Package flow implements Sophon's command core: short-lived, lock-holding
// operations over the filesystem protocol in internal/store. Every operation
// is re-runnable: typed intent is published before external effects and
// receipts after, so repeating a command converges via observed reality.
package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sophon/internal/delivery"
	gitcontrol "sophon/internal/git"
	"sophon/internal/herdr"
	"sophon/internal/naming"
	"sophon/internal/store"
	"sophon/internal/treehouse"
	"sophon/internal/validation"
)

var (
	// ErrAttemptsExist refuses a plain spawn over an existing attempt.
	ErrAttemptsExist = errors.New("task already has attempts; re-run with retry to fence the current attempt and spawn again")
	// ErrNotReady refuses verification when the current attempt has no result.
	ErrNotReady = errors.New("current attempt has no published result")
	// ErrStaleAttempt refuses to act on a result published to a fenced,
	// non-current attempt. Nothing is mutated.
	ErrStaleAttempt = errors.New("result exists only in a fenced non-current attempt")
	// ErrLeaseConflict marks a live Treehouse lease identity mismatch.
	ErrLeaseConflict = errors.New("treehouse lease identity mismatch")
	// ErrNotConfirmed refuses a delivery effect without operator confirmation.
	ErrNotConfirmed = errors.New("delivery requires operator confirmation (--confirmed)")
	// ErrHeadMismatch marks drift between a recorded head and observed Git.
	ErrHeadMismatch = errors.New("head SHA does not match the verified attempt head")
	// ErrInvalidResult marks a worker result that fails the strict schema.
	ErrInvalidResult = errors.New("invalid worker result")
	// ErrInvalidReport marks typed non-completion evidence that fails its
	// strict schema or identity contract.
	ErrInvalidReport = errors.New("invalid worker report")
	// ErrEvidenceConflict refuses differing or completion-vs-report evidence
	// for the same attempt. The command never chooses by timestamp.
	ErrEvidenceConflict = errors.New("worker evidence conflict")
	// ErrReconciliation marks public PR/branch state that no longer matches the
	// exact continuation identity Sophon recorded.
	ErrReconciliation = errors.New("open pull request requires reconciliation")
)

// Git is the subset of internal/git.Client the flow needs.
type Git interface {
	CreateTaskBranch(context.Context, string, string) (gitcontrol.Snapshot, error)
	CreateTaskBranchAt(context.Context, string, string, string, string) (gitcontrol.Snapshot, error)
	Snapshot(context.Context, string) (gitcontrol.Snapshot, error)
	VerifyCompletion(context.Context, string, string) (gitcontrol.Completion, error)
}

// DeliveryGit matches internal/delivery.CommandGit's immutable-head checks.
type DeliveryGit interface {
	VerifyHead(context.Context, string, string, string) error
	Repository(context.Context, string) (string, error)
	CommitMessages(context.Context, string, string, string) ([]string, error)
	FetchBranch(context.Context, string, string, string) error
	VerifyStrictDescendant(context.Context, string, string, string) error
}

// DeliveryRemote matches internal/delivery.CommandRemote's forge boundary.
type DeliveryRemote interface {
	Push(context.Context, string, string, string, string) error
	PushFastForward(context.Context, string, string, string, string, string) error
	FindPullRequest(context.Context, string, string, string, string) (*delivery.PullRequest, error)
	CreatePullRequest(context.Context, delivery.PullRequestInput) (delivery.PullRequest, error)
	ObservePullRequest(context.Context, string, int) (delivery.PullRequest, error)
	HeadSHA(context.Context, string, string, string) (string, error)
	BranchHead(context.Context, string, string, string) (string, bool, error)
	DefaultBranch(context.Context, string, string) (string, error)
}

// Validator runs the task's validation command in a worktree.
type Validator interface {
	Run(context.Context, string) (validation.Result, error)
}

// Deps wires the flow's external boundaries. Tests substitute fakes.
type Deps struct {
	Git            Git
	Leases         treehouse.CLI
	Panes          herdr.Adapter
	DeliveryGit    DeliveryGit
	DeliveryRemote DeliveryRemote
	NewValidator   func(command string) Validator
	// HerdrSession is the explicit Herdr session spawn placements target.
	HerdrSession string
	// NewSessionPanes builds an exact-session pane boundary for volatile
	// commander routing and worker pane retirement. Nil disables both.
	NewSessionPanes func(session string) SessionPanes
	// Model optionally selects the worker runtime model.
	Model string
}

// Flow is the command core. It is stateless; all truth lives in the store.
type Flow struct {
	deps Deps
}

func New(deps Deps) *Flow {
	if deps.NewValidator == nil {
		deps.NewValidator = ShellValidatorFactory
	}
	return &Flow{deps: deps}
}

// ShellValidatorFactory builds the production /bin/sh validator for a
// configured validation command.
func ShellValidatorFactory(command string) Validator {
	return validation.ShellValidator(validation.ProjectValidation, "shell", command)
}

// ProductionDeps wires the existing pure packages. The pane adapter is built
// by the caller because it carries Herdr session routing.
func ProductionDeps(gitBinary, treehouseBinary, ghBinary string, panes herdr.Adapter) Deps {
	git := gitcontrol.NewClient()
	git.Binary = gitBinary
	return Deps{
		Git:            git,
		Leases:         treehouse.NewCommandClient(treehouseBinary),
		Panes:          panes,
		DeliveryGit:    delivery.CommandGit{Binary: gitBinary},
		DeliveryRemote: delivery.CommandRemote{GitBinary: gitBinary, GHBinary: ghBinary},
	}
}

// LeaseHolder mirrors the attempt-scoped lease identity used since the old
// control plane: "sophon:<task-id>:<attempt>".
func LeaseHolder(taskID string, attempt int) string {
	return fmt.Sprintf("sophon:%s:%d", taskID, attempt)
}

// TaskBranch mirrors the established task branch format
// "sophon/<name>/attempt-<n>".
func TaskBranch(title, taskID string, attempt int) string {
	return fmt.Sprintf("sophon/%s/attempt-%d", naming.TaskName(title, taskID), attempt)
}

// taskAndMission loads a task and its mission by task ID alone.
func (f *Flow) taskAndMission(taskID string) (store.Task, store.Mission, error) {
	task, err := store.FindTask(taskID)
	if err != nil {
		return store.Task{}, store.Mission{}, err
	}
	mission, err := store.ReadMission(task.MissionID)
	if err != nil {
		return store.Task{}, store.Mission{}, err
	}
	return task, mission, nil
}

// currentAttempt returns the task's incarnation token, refusing tasks that
// have never spawned.
func currentAttempt(task store.Task) (int, error) {
	if task.CurrentAttempt < 1 {
		return 0, fmt.Errorf("%w: task %s has no attempts; spawn first", ErrNotReady, task.ID)
	}
	return task.CurrentAttempt, nil
}

func requireNonEmpty(fields ...string) error {
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			return errors.New("required argument is empty")
		}
	}
	return nil
}
