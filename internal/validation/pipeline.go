package validation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"parallel-intellect/internal/domain"
)

type Pipeline struct {
	Store       Store
	Workspace   WorkspaceFingerprinter
	Environment EnvironmentFingerprinter
}

type Request struct {
	TaskID     domain.TaskID
	CommandID  domain.CommandID
	Validators []Validator
	// Config is the canonical validation configuration. It is hashed rather
	// than persisted in a cache key, so callers may include sensitive values.
	Config []byte
	Actor  string
}

type Run struct {
	Record Record `json:"record"`
	Cached bool   `json:"cached"`
}

type Report struct {
	Task      domain.Task `json:"task"`
	Runs      []Run       `json:"runs"`
	Passed    bool        `json:"passed"`
	CacheHits int         `json:"cache_hits"`
}

func (p *Pipeline) ValidateTask(ctx context.Context, request Request) (Report, error) {
	if p == nil || p.Store == nil || p.Workspace == nil || p.Environment == nil {
		return Report{}, errors.New("validation pipeline is not fully configured")
	}
	if request.TaskID == "" || request.CommandID == "" || len(request.Validators) == 0 {
		return Report{}, errors.New("task, command ID, and at least one validator are required")
	}
	if request.Actor == "" {
		request.Actor = "commander"
	}
	task, err := p.Store.Task(ctx, request.TaskID)
	if err != nil {
		return Report{}, err
	}
	if task.DeliveryMode == domain.DeliveryBranch {
		return Report{}, errors.New("branch-only tasks do not enter validation")
	}
	if task.State != domain.TaskReady && task.State != domain.TaskDeliveryBlocked && task.State != domain.TaskValidating {
		return Report{}, fmt.Errorf("validate task while %s", task.State)
	}
	attempt, err := p.Store.Attempt(ctx, task.ID, task.CurrentAttempt)
	if err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(attempt.WorktreePath) == "" || strings.TrimSpace(attempt.HeadSHA) == "" {
		return Report{}, errors.New("task attempt has no completed worktree and head SHA")
	}
	workspace, err := p.Workspace.Fingerprint(ctx, attempt.WorktreePath)
	if err != nil {
		return Report{}, err
	}
	if !strings.EqualFold(workspace.HeadSHA, attempt.HeadSHA) {
		return Report{}, fmt.Errorf("validation HEAD %s does not match attempt head %s", workspace.HeadSHA, attempt.HeadSHA)
	}
	environmentHash, err := p.Environment.Fingerprint()
	if err != nil {
		return Report{}, fmt.Errorf("fingerprint validation environment: %w", err)
	}
	configHash, err := hashJSON(request.Config)
	if err != nil {
		return Report{}, fmt.Errorf("hash validation config: %w", err)
	}

	validating, err := p.Store.BeginValidation(ctx, childCommand(request.CommandID, "start", ""), BeginInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, Actor: request.Actor,
	})
	if err != nil {
		return Report{}, err
	}
	report := Report{Passed: true, Runs: make([]Run, 0, len(request.Validators))}
	runIDs := make([]string, 0, len(request.Validators))
	runIDSet := make(map[string]struct{}, len(request.Validators))
	for _, validator := range request.Validators {
		if validator == nil {
			return Report{}, errors.New("nil validator")
		}
		if err := validateKind(validator.Kind()); err != nil {
			return Report{}, err
		}
		if strings.TrimSpace(validator.Version()) == "" || len(validator.Command()) == 0 {
			return Report{}, errors.New("validator version and command are required")
		}
		commandHash, err := hashJSON(validator.Command())
		if err != nil {
			return Report{}, fmt.Errorf("hash validator command: %w", err)
		}
		key := Key{
			TaskID: task.ID, HeadSHA: strings.ToLower(workspace.HeadSHA), WorkspaceHash: workspace.DirtyTreeHash,
			Validator: validator.Kind(), ValidatorVersion: validator.Version(), ConfigHash: configHash,
			CommandHash: commandHash, EnvironmentHash: environmentHash,
		}
		if err := key.Validate(); err != nil {
			return Report{}, err
		}
		record, err := p.Store.LookupValidation(ctx, key)
		cached := err == nil && record != nil
		if err != nil {
			return Report{}, err
		}
		if !cached {
			result, runErr := validator.Run(ctx, attempt.WorktreePath)
			if runErr != nil {
				return Report{}, fmt.Errorf("run %s validator: %w", validator.Kind(), runErr)
			}
			if err := p.verifyWorkspace(ctx, attempt.WorktreePath, workspace); err != nil {
				return Report{}, err
			}
			if err := validateResult(result); err != nil {
				return Report{}, fmt.Errorf("invalid %s validator result: %w", validator.Kind(), err)
			}
			stored, storeErr := p.Store.RecordValidation(ctx,
				childCommand(request.CommandID, "record", key.Digest()),
				RecordInput{TaskID: task.ID, Attempt: task.CurrentAttempt, Key: key, Result: result})
			if storeErr != nil {
				return Report{}, storeErr
			}
			record = &stored
		} else {
			report.CacheHits++
		}
		if _, duplicate := runIDSet[record.ID]; !duplicate {
			runIDs = append(runIDs, record.ID)
			runIDSet[record.ID] = struct{}{}
		}
		report.Runs = append(report.Runs, Run{Record: *record, Cached: cached})
		if record.Status != Passed {
			report.Passed = false
		}
	}
	if err := p.verifyWorkspace(ctx, attempt.WorktreePath, workspace); err != nil {
		return Report{}, err
	}
	completed, err := p.Store.CompleteValidation(ctx, childCommand(request.CommandID, "complete", ""), CompleteInput{
		TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedVersion: validating.Version,
		HeadSHA: strings.ToLower(workspace.HeadSHA), WorkspaceHash: workspace.DirtyTreeHash,
		ConfigHash: configHash, EnvironmentHash: environmentHash, RunIDs: runIDs, Actor: request.Actor,
	})
	if err != nil {
		return Report{}, err
	}
	report.Task = completed
	return report, nil
}

func (p *Pipeline) verifyWorkspace(ctx context.Context, path string, expected Workspace) error {
	current, err := p.Workspace.Fingerprint(ctx, path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(current.HeadSHA, expected.HeadSHA) || current.DirtyTreeHash != expected.DirtyTreeHash {
		return errors.New("validation workspace changed while validators were running")
	}
	return nil
}

func childCommand(parent domain.CommandID, phase, suffix string) domain.CommandID {
	value := string(parent) + ":validation:" + phase
	if suffix != "" {
		value += ":" + suffix[:16]
	}
	return domain.CommandID(value)
}
