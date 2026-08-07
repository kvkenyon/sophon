package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sophon/internal/domain"
)

type Service struct {
	Store  Store
	Git    LocalGit
	Remote Remote
	Gate   Gate
	Leases LeaseReleaser
}

type Request struct {
	TaskID    domain.TaskID
	CommandID domain.CommandID
	Base      string
	Actor     string
}

func (s *Service) Deliver(ctx context.Context, in Request) (Result, error) {
	if s == nil || s.Store == nil || s.Git == nil {
		return Result{}, errors.New("delivery service is not fully configured")
	}
	if in.TaskID == "" || in.CommandID == "" {
		return Result{}, errors.New("task and command ID are required")
	}
	if in.Actor == "" {
		in.Actor = "operator"
	}
	reservation, err := s.Store.ReserveDelivery(ctx, in.CommandID, ReserveInput{
		TaskID: in.TaskID, Operation: "deliver", Base: in.Base, Actor: in.Actor,
	})
	if err != nil {
		return Result{}, err
	}
	target, err := s.Store.DeliveryTarget(ctx, reservation.TaskID, reservation.Attempt)
	if err != nil {
		return Result{}, err
	}
	existing, err := s.Store.Delivery(ctx, target.Task.ID, target.Attempt.Attempt)
	if err != nil {
		return Result{}, err
	}
	if existing != nil && (existing.State == StateDelivered || existing.State == StateDeliveredBranch) {
		return Result{Task: target.Task, Delivery: *existing}, nil
	}
	if existing != nil && existing.State == StateBlocked && existing.CommandID == in.CommandID {
		return Result{Task: target.Task, Delivery: *existing}, ErrGateFailed
	}
	if strings.TrimSpace(target.Attempt.HeadSHA) == "" || strings.TrimSpace(target.Attempt.Branch) == "" || strings.TrimSpace(target.Attempt.WorktreePath) == "" {
		return Result{}, errors.New("task attempt has no completed branch, head SHA, and worktree")
	}
	if err := s.Git.VerifyHead(ctx, target.Attempt.WorktreePath, target.Attempt.Branch, target.Attempt.HeadSHA); err != nil {
		return Result{}, err
	}

	repository := ""
	if target.Task.DeliveryMode != domain.DeliveryBranch {
		if s.Remote == nil {
			return Result{}, errors.New("remote delivery is not configured")
		}
		repository, err = s.Git.Repository(ctx, target.Attempt.WorktreePath)
		if err != nil {
			return Result{}, err
		}
	}
	current := Result{Task: target.Task}
	if existing == nil || existing.State == StateBlocked {
		current, err = s.Store.PrepareDelivery(ctx, childCommand(in.CommandID, "prepare"), PrepareInput{
			TaskID: target.Task.ID, Attempt: target.Attempt.Attempt, ExpectedVersion: target.Task.Version,
			Mode: target.Task.DeliveryMode, Repository: repository, Branch: target.Attempt.Branch,
			HeadSHA: strings.ToLower(target.Attempt.HeadSHA), RequestCommandID: in.CommandID,
			RequestBase: reservation.Base, Actor: in.Actor,
		})
		if err != nil {
			return Result{}, err
		}
	} else {
		current.Delivery = *existing
	}
	if current.Delivery.State == StateDeliveredBranch {
		return current, nil
	}
	if current.Delivery.Mode == domain.DeliveryGate && current.Delivery.GateState != GatePassed {
		if s.Gate == nil {
			return Result{}, errors.New("no-mistakes gate is not configured")
		}
		gate, gateErr := s.Gate.Run(ctx, target.Attempt.WorktreePath, target.Task.Objective)
		if gateErr != nil {
			return Result{}, gateErr
		}
		current, err = s.Store.RecordDeliveryGate(ctx, childCommand(in.CommandID, "gate"), GateInput{
			TaskID: target.Task.ID, Attempt: target.Attempt.Attempt, HeadSHA: strings.ToLower(target.Attempt.HeadSHA),
			Passed: gate.Passed, Output: gate.Output, Actor: in.Actor,
		})
		if err != nil {
			return Result{}, err
		}
		if !gate.Passed {
			return current, ErrGateFailed
		}
		// A gate may run fixers. Never let such a mutation silently retarget delivery.
		if err := s.Git.VerifyHead(ctx, target.Attempt.WorktreePath, target.Attempt.Branch, target.Attempt.HeadSHA); err != nil {
			return Result{}, err
		}
	}

	head := strings.ToLower(target.Attempt.HeadSHA)
	if err := s.Remote.Push(ctx, repository, target.Attempt.WorktreePath, target.Attempt.Branch, head); err != nil {
		return Result{}, fmt.Errorf("push verified delivery head: %w", err)
	}
	pull, err := s.Remote.FindPullRequest(ctx, repository, target.Attempt.WorktreePath, target.Attempt.Branch, head)
	if err != nil {
		return Result{}, fmt.Errorf("reconcile pull request: %w", err)
	}
	if pull == nil {
		created, createErr := s.Remote.CreatePullRequest(ctx, PullRequestInput{
			Repository: repository, Worktree: target.Attempt.WorktreePath, Branch: target.Attempt.Branch,
			HeadSHA: head, Base: reservation.Base, Title: target.Task.Title, Body: target.Task.Objective,
		})
		if createErr != nil {
			return Result{}, fmt.Errorf("create pull request: %w", createErr)
		}
		pull = &created
	}
	if pull.Repository != repository || pull.Branch != target.Attempt.Branch || !strings.EqualFold(pull.HeadSHA, head) {
		return Result{}, errors.New("reconciled pull request does not match repository, branch, and verified head")
	}
	remoteHead, err := s.Remote.HeadSHA(ctx, repository, target.Attempt.WorktreePath, target.Attempt.Branch)
	if err != nil {
		return Result{}, fmt.Errorf("verify remote delivery head: %w", err)
	}
	if !strings.EqualFold(remoteHead, head) {
		return Result{}, fmt.Errorf("%w: remote %s, attempt %s", ErrHeadMismatch, remoteHead, head)
	}
	return s.Store.CompleteDelivery(ctx, childCommand(in.CommandID, "complete"), CompleteInput{
		TaskID: target.Task.ID, Attempt: target.Attempt.Attempt, Repository: repository,
		Branch: target.Attempt.Branch, HeadSHA: head, PRURL: pull.URL, PRNumber: pull.Number, Actor: in.Actor,
	})
}

// Reconcile resumes an interrupted delivery through the original M10 command
// and its persisted request inputs. Push and PR lookup remain externally
// idempotent, including the crash window after PR creation but before SQLite.
func (s *Service) Reconcile(ctx context.Context, taskID domain.TaskID, attempt int) (Result, error) {
	if s == nil || s.Store == nil {
		return Result{}, errors.New("delivery reconciler is not fully configured")
	}
	record, err := s.Store.Delivery(ctx, taskID, attempt)
	if err != nil {
		return Result{}, err
	}
	if record == nil {
		return Result{}, errors.New("interrupted delivery has no durable intent")
	}
	if record.State == StateDelivered || record.State == StateDeliveredBranch {
		target, err := s.Store.DeliveryTarget(ctx, taskID, attempt)
		if err != nil {
			return Result{}, err
		}
		return Result{Task: target.Task, Delivery: *record}, nil
	}
	if record.Mode == domain.DeliveryGate && record.GateState == GatePending {
		target, err := s.Store.DeliveryTarget(ctx, taskID, attempt)
		if err != nil {
			return Result{}, err
		}
		return Result{Task: target.Task, Delivery: *record}, ErrGateRecoveryRequired
	}
	return s.Deliver(ctx, Request{TaskID: taskID, CommandID: record.CommandID,
		Base: record.RequestBase, Actor: record.RequestActor})
}

func (s *Service) Release(ctx context.Context, taskID domain.TaskID, commandID domain.CommandID, actor string) (domain.TreehouseLease, error) {
	if s == nil || s.Store == nil || s.Leases == nil {
		return domain.TreehouseLease{}, errors.New("delivery release is not fully configured")
	}
	if taskID == "" || commandID == "" {
		return domain.TreehouseLease{}, errors.New("task and command ID are required")
	}
	if actor == "" {
		actor = "operator"
	}
	reservation, err := s.Store.ReserveDelivery(ctx, commandID, ReserveInput{TaskID: taskID, Operation: "release", Actor: actor})
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	target, err := s.Store.DeliveryTarget(ctx, taskID, reservation.Attempt)
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	if target.Task.DeliveryMode != domain.DeliveryBranch || target.Task.State != domain.TaskDeliveredBranch {
		return domain.TreehouseLease{}, errors.New("only delivered_branch tasks may release their retained lease")
	}
	lease, err := s.Store.TreehouseLease(ctx, taskID, reservation.Attempt)
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	record, err := s.Store.Delivery(ctx, taskID, reservation.Attempt)
	if err != nil || record == nil {
		return domain.TreehouseLease{}, errors.New("branch delivery record is missing")
	}
	if lease.State == domain.TreehouseLeaseReleased && record.ReleaseState == "completed" {
		return lease, nil
	}
	intent := ReleaseIntentInput{TaskID: taskID, Attempt: reservation.Attempt, LeaseID: lease.LeaseID,
		LeaseHolder: lease.LeaseHolder, RequestCommandID: commandID, Actor: actor}
	if record.ReleaseState == "pending" {
		intent.RequestCommandID = record.ReleaseCommandID
		intent.Actor = record.ReleaseActor
	}
	if lease.State != domain.TreehouseLeaseActive && lease.State != domain.TreehouseLeaseReleased {
		return domain.TreehouseLease{}, errors.New("retained task lease is not active")
	}
	if record.ReleaseState != "pending" {
		if err := s.Store.PrepareDeliveryRelease(ctx, childCommand(intent.RequestCommandID, "release-prepare"), intent); err != nil {
			return domain.TreehouseLease{}, err
		}
	}
	if lease.State == domain.TreehouseLeaseActive {
		lease, err = s.Leases.Release(ctx, childCommand(intent.RequestCommandID, "lease"), taskID, reservation.Attempt)
		if err != nil {
			return domain.TreehouseLease{}, err
		}
	}
	if err := s.Store.CompleteDeliveryRelease(ctx, childCommand(intent.RequestCommandID, "release-complete"), intent); err != nil {
		return domain.TreehouseLease{}, err
	}
	return lease, nil
}

func childCommand(parent domain.CommandID, phase string) domain.CommandID {
	return domain.CommandID(string(parent) + ":delivery:" + phase)
}
