package treehouse

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sophon/internal/db"
	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/id"
	"sophon/internal/naming"
)

type GitInspector interface {
	CreateTaskBranch(context.Context, string, string) (gitcontrol.Snapshot, error)
	Snapshot(context.Context, string) (gitcontrol.Snapshot, error)
}

type Service struct {
	store *db.Store
	cli   CLI
	git   GitInspector
}

const TaskBranchPrefix = "sophon/"

func NewService(store *db.Store, cli CLI, git GitInspector) *Service {
	return &Service{store: store, cli: cli, git: git}
}

func LeaseHolder(taskID domain.TaskID, attempt int) string {
	return fmt.Sprintf("sophon:%s:%d", taskID, attempt)
}

func legacyLeaseHolder(taskID domain.TaskID, attempt int) string {
	// Accept the former prefix only while identifying an attempt-scoped lease
	// acquired before the rename. Once persisted, all safety checks continue to
	// compare the exact recorded holder so a replacement lease is never touched.
	return fmt.Sprintf("parallel-intellect:%s:%d", taskID, attempt)
}

func leaseHolderMatchesAttempt(holder string, taskID domain.TaskID, attempt int) bool {
	return holder == LeaseHolder(taskID, attempt) || holder == legacyLeaseHolder(taskID, attempt)
}

// Acquire allocates externally, inspects Git, and then atomically persists the
// lease. Any failure after allocation is compensated only with an identity- and
// holder-guarded return.
func (s *Service) Acquire(ctx context.Context, commandID domain.CommandID, taskID domain.TaskID, attempt int) (domain.TreehouseLease, error) {
	if s.store == nil || s.cli == nil || s.git == nil {
		return domain.TreehouseLease{}, errors.New("Treehouse service is not fully configured")
	}
	if commandID == "" || taskID == "" || attempt < 1 {
		return domain.TreehouseLease{}, errors.New("command, task, and attempt are required")
	}
	target, err := s.store.LeaseTarget(ctx, taskID, attempt)
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	if target.Existing != nil {
		if target.Existing.State != domain.TreehouseLeaseActive {
			return domain.TreehouseLease{}, db.ErrLeaseExists
		}
		return *target.Existing, nil
	}

	holder := LeaseHolder(taskID, attempt)
	allocation, err := s.cli.Acquire(ctx, target.ProjectPath, holder)
	if err != nil {
		if completeAllocation(allocation) {
			cleanupCtx, cancel := cleanupContext(ctx)
			defer cancel()
			if releaseErr := s.cli.Release(cleanupCtx, target.ProjectPath, allocation); releaseErr != nil {
				return domain.TreehouseLease{}, errors.Join(err,
					fmt.Errorf("conditionally return rejected lease: %w", releaseErr))
			}
		}
		return domain.TreehouseLease{}, err
	}
	compensate := func(cause error) error {
		cleanupCtx, cancel := cleanupContext(ctx)
		defer cancel()
		releaseErr := s.cli.Release(cleanupCtx, target.ProjectPath, allocation)
		if releaseErr != nil {
			return errors.Join(cause, fmt.Errorf("conditionally return unpersisted lease: %w", releaseErr))
		}
		return cause
	}
	branch := TaskBranch(target.TaskTitle, taskID, attempt)
	snapshot, err := s.git.CreateTaskBranch(ctx, allocation.WorktreePath, branch)
	if err != nil {
		return domain.TreehouseLease{}, compensate(fmt.Errorf("inspect acquired worktree: %w", err))
	}
	if !snapshot.Clean {
		return domain.TreehouseLease{}, compensate(errors.New("acquired Treehouse worktree is not clean"))
	}
	if snapshot.Branch != branch {
		return domain.TreehouseLease{}, compensate(fmt.Errorf("created task branch %q, got %q", branch, snapshot.Branch))
	}
	lease := domain.TreehouseLease{
		LeaseID: allocation.LeaseID, TaskID: taskID, Attempt: attempt,
		LeaseHolder: allocation.LeaseHolder, WorktreePath: allocation.WorktreePath,
		Project: target.Project, ProjectPath: target.ProjectPath,
		Branch: snapshot.Branch, BaseSHA: snapshot.Head, AcquiredAt: allocation.LeasedAt,
	}
	lease, err = s.store.RecordTreehouseLease(ctx, commandID, db.RecordTreehouseLeaseInput{
		TaskID: taskID, Attempt: attempt, ExpectedVersion: target.ExpectedVersion,
		Lease: lease, Actor: "scheduler",
	})
	if err != nil {
		return domain.TreehouseLease{}, compensate(fmt.Errorf("persist acquired Treehouse lease: %w", err))
	}
	lease.ProjectPath = target.ProjectPath
	return lease, nil
}

// TaskBranch applies the product-wide task-branch convention. Each attempt
// gets its own branch because attempts are independently leased and fenced.
func TaskBranch(title string, taskID domain.TaskID, attempt int) string {
	return fmt.Sprintf("%s%s/attempt-%d", TaskBranchPrefix, naming.TaskName(title, string(taskID)), attempt)
}

// Release refuses stale attempts before invoking Treehouse. The external call
// always carries the persisted lease ID and holder; path is never sufficient.
func (s *Service) Release(ctx context.Context, commandID domain.CommandID, taskID domain.TaskID, attempt int) (domain.TreehouseLease, error) {
	if s.store == nil || s.cli == nil {
		return domain.TreehouseLease{}, errors.New("Treehouse service is not fully configured")
	}
	if commandID == "" || taskID == "" || attempt < 1 {
		return domain.TreehouseLease{}, errors.New("command, task, and attempt are required")
	}
	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	if task.CurrentAttempt != attempt {
		return domain.TreehouseLease{}, db.ErrStaleAttempt
	}
	lease, err := s.store.TreehouseLease(ctx, taskID, attempt)
	if err != nil {
		return domain.TreehouseLease{}, err
	}
	if lease.State == domain.TreehouseLeaseReleased {
		return lease, nil
	}
	if lease.State != domain.TreehouseLeaseActive {
		return domain.TreehouseLease{}, db.ErrLeaseConflict
	}
	allocation := Allocation{WorktreePath: lease.WorktreePath, LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder}
	if err := s.cli.Release(ctx, lease.ProjectPath, allocation); err != nil {
		// The process may have died after Treehouse accepted a conditional
		// return but before SQLite recorded it. Re-observe the exact identity:
		// if it is no longer live, finish only our old durable lease record and
		// never issue an operation against a replacement holder.
		statuses, observeErr := s.cli.Status(ctx, lease.ProjectPath)
		if observeErr != nil {
			return domain.TreehouseLease{}, errors.Join(err,
				fmt.Errorf("reconcile conditional Treehouse return: %w", observeErr))
		}
		for _, status := range statuses {
			if status.WorktreePath == lease.WorktreePath && status.Status == "leased" &&
				status.LeaseID == lease.LeaseID && status.LeaseHolder == lease.LeaseHolder {
				return domain.TreehouseLease{}, err
			}
		}
	}
	persistCtx, cancel := cleanupContext(ctx)
	defer cancel()
	released, err := s.store.MarkTreehouseLeaseReleased(persistCtx, commandID, db.ReleaseTreehouseLeaseInput{
		TaskID: taskID, Attempt: attempt, LeaseID: lease.LeaseID,
		LeaseHolder: lease.LeaseHolder, Actor: "scheduler",
	})
	if err != nil {
		return domain.TreehouseLease{}, fmt.Errorf("persist released Treehouse lease: %w", err)
	}
	return released, nil
}

type ReconcileResult struct {
	Valid    int
	Adopted  int
	Awaiting int
	Released int
	Fenced   int
	Missing  int
}

// Reconcile compares durable active leases with Treehouse status. A mismatch
// is fenced in SQLite and never passed to Release, so a new holder is untouched.
func (s *Service) Reconcile(ctx context.Context) (ReconcileResult, error) {
	if s.store == nil || s.cli == nil || s.git == nil {
		return ReconcileResult{}, errors.New("Treehouse service is not fully configured")
	}
	leases, err := s.store.ActiveTreehouseLeases(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	targets, err := s.store.UnleasedProvisioningTargets(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	byProject := make(map[string][]domain.TreehouseLease)
	for _, lease := range leases {
		byProject[lease.ProjectPath] = append(byProject[lease.ProjectPath], lease)
	}
	targetsByProject := make(map[string][]db.LeaseAcquisitionTarget)
	for _, target := range targets {
		targetsByProject[target.ProjectPath] = append(targetsByProject[target.ProjectPath], target)
		if _, exists := byProject[target.ProjectPath]; !exists {
			byProject[target.ProjectPath] = nil
		}
	}
	var result ReconcileResult
	for projectPath, projectLeases := range byProject {
		statuses, err := s.cli.Status(ctx, projectPath)
		if err != nil {
			return result, fmt.Errorf("inspect Treehouse project %s: %w", projectPath, err)
		}
		byPath := make(map[string]WorktreeStatus, len(statuses))
		for _, status := range statuses {
			byPath[status.WorktreePath] = status
		}
		for _, lease := range projectLeases {
			observed, exists := byPath[lease.WorktreePath]
			matching := exists && observed.Status == "leased" && observed.LeaseID == lease.LeaseID &&
				observed.LeaseHolder == lease.LeaseHolder
			pendingRelease, err := s.store.PendingDeliveryRelease(ctx, lease.TaskID, lease.Attempt,
				lease.LeaseID, lease.LeaseHolder)
			if err != nil {
				return result, err
			}
			if pendingRelease != nil {
				if matching {
					if err := s.cli.Release(ctx, lease.ProjectPath, Allocation{WorktreePath: lease.WorktreePath,
						LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder}); err != nil {
						return result, fmt.Errorf("resume interrupted Treehouse release for %s: %w", lease.LeaseID, err)
					}
				}
				commandID, err := newCommandID()
				if err != nil {
					return result, err
				}
				if _, err := s.store.MarkTreehouseLeaseReleased(ctx, commandID, db.ReleaseTreehouseLeaseInput{
					TaskID: lease.TaskID, Attempt: lease.Attempt, LeaseID: lease.LeaseID,
					LeaseHolder: lease.LeaseHolder, Actor: "recovery",
				}); err != nil {
					return result, fmt.Errorf("finish interrupted Treehouse release for %s: %w", lease.LeaseID, err)
				}
				completeCommand := domain.CommandID(string(pendingRelease.RequestCommandID) + ":delivery:release-complete")
				if err := s.store.CompleteDeliveryRelease(ctx, completeCommand, *pendingRelease); err != nil {
					return result, fmt.Errorf("complete interrupted delivery release for %s: %w", lease.LeaseID, err)
				}
				result.Released++
				continue
			}
			if matching {
				result.Valid++
				continue
			}
			outcome := domain.TreehouseLeaseFenced
			if !exists || observed.Status == "missing" || observed.Status == "orphaned" {
				outcome = domain.TreehouseLeaseMissing
			}
			commandID, err := newCommandID()
			if err != nil {
				return result, err
			}
			if _, err := s.store.ReconcileTreehouseLease(ctx, commandID, db.ReconcileTreehouseLeaseInput{
				TaskID: lease.TaskID, Attempt: lease.Attempt, LeaseID: lease.LeaseID,
				LeaseHolder: lease.LeaseHolder, Outcome: outcome, Actor: "recovery",
			}); err != nil {
				return result, fmt.Errorf("persist Treehouse reconciliation for %s: %w", lease.LeaseID, err)
			}
			if outcome == domain.TreehouseLeaseMissing {
				result.Missing++
			} else {
				result.Fenced++
			}
		}
		for _, target := range targetsByProject[projectPath] {
			holder := LeaseHolder(target.TaskID, target.Attempt)
			var observed *WorktreeStatus
			for index := range statuses {
				status := &statuses[index]
				if status.Status == "leased" && leaseHolderMatchesAttempt(status.LeaseHolder, target.TaskID, target.Attempt) {
					if observed != nil {
						return result, fmt.Errorf("multiple Treehouse leases found for %s", holder)
					}
					observed = status
				}
			}
			if observed == nil {
				result.Awaiting++
				continue
			}
			if observed.LeaseID == "" || observed.WorktreePath == "" {
				return result, fmt.Errorf("Treehouse recovery found incomplete lease for %s", holder)
			}
			snapshot, err := s.git.Snapshot(ctx, observed.WorktreePath)
			if err != nil {
				return result, fmt.Errorf("inspect unrecorded Treehouse lease %s: %w", observed.LeaseID, err)
			}
			branch := TaskBranch(target.TaskTitle, target.TaskID, target.Attempt)
			if !snapshot.Clean || snapshot.Branch != branch {
				return result, fmt.Errorf("unrecorded Treehouse lease %s has unexpected branch or dirty worktree", observed.LeaseID)
			}
			acquiredAt := time.Now().UTC()
			if observed.LeasedAt != nil {
				acquiredAt = observed.LeasedAt.UTC()
			}
			commandID, err := newCommandID()
			if err != nil {
				return result, err
			}
			if _, err := s.store.RecordTreehouseLease(ctx, commandID, db.RecordTreehouseLeaseInput{
				TaskID: target.TaskID, Attempt: target.Attempt, ExpectedVersion: target.ExpectedVersion,
				Lease: domain.TreehouseLease{LeaseID: observed.LeaseID, LeaseHolder: observed.LeaseHolder,
					WorktreePath: observed.WorktreePath, Project: target.Project, Branch: branch,
					BaseSHA: snapshot.Head, AcquiredAt: acquiredAt}, Actor: "recovery",
			}); err != nil {
				return result, fmt.Errorf("adopt unrecorded Treehouse lease %s: %w", observed.LeaseID, err)
			}
			result.Adopted++
		}
	}
	return result, nil
}

func newCommandID() (domain.CommandID, error) {
	raw, err := id.New("cmd")
	if err != nil {
		return "", fmt.Errorf("generate reconciliation command ID: %w", err)
	}
	return domain.CommandID(raw), nil
}

func completeAllocation(allocation Allocation) bool {
	return allocation.WorktreePath != "" && allocation.LeaseID != "" && allocation.LeaseHolder != ""
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}
