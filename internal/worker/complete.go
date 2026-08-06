package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/treehouse"
)

var (
	ErrInvalidResult = errors.New("invalid worker result")
	ErrStaleResult   = errors.New("stale worker result")
	ErrHeadMismatch  = errors.New("reported head SHA does not match worktree HEAD")
)

type GitVerifier interface {
	VerifyCompletion(context.Context, string, string) (gitcontrol.Completion, error)
}

type LeaseObserver interface {
	Status(context.Context, string) ([]treehouse.WorktreeStatus, error)
}

type Completer struct {
	Store     *db.Store
	Git       GitVerifier
	Leases    LeaseObserver
	TaskFiles BriefGenerator
}

type CompleteRequest struct {
	TaskID     domain.TaskID
	Attempt    int
	HeadSHA    string
	ResultPath string
	CommandID  domain.CommandID
}

func (c *Completer) Complete(ctx context.Context, in CompleteRequest) (domain.Task, error) {
	if c == nil || c.Store == nil || c.Git == nil || c.Leases == nil {
		return domain.Task{}, errors.New("worker completer is not fully configured")
	}
	if in.TaskID == "" || in.Attempt < 1 || strings.TrimSpace(in.HeadSHA) == "" ||
		strings.TrimSpace(in.ResultPath) == "" || in.CommandID == "" {
		return domain.Task{}, errors.New("task, attempt, head SHA, result, and command ID are required")
	}
	task, err := c.Store.Task(ctx, in.TaskID)
	if err != nil {
		return domain.Task{}, err
	}
	if task.CurrentAttempt != in.Attempt {
		return domain.Task{}, db.ErrStaleAttempt
	}
	if task.Kind != domain.TaskImplementation {
		return domain.Task{}, errors.New("milestone 3 completes only implementation tasks")
	}
	expectedVersion := task.Version
	if task.State == domain.TaskReady {
		// A retry of the same command must reproduce the original command
		// request so the store can return its durable result without mutating.
		expectedVersion -= 2
	} else if task.State != domain.TaskRunning {
		return domain.Task{}, fmt.Errorf("complete task while %s", task.State)
	}
	attempt, err := c.Store.Attempt(ctx, in.TaskID, in.Attempt)
	if err != nil {
		return domain.Task{}, err
	}
	lease, err := c.Store.TreehouseLease(ctx, in.TaskID, in.Attempt)
	if err != nil {
		return domain.Task{}, err
	}
	if lease.State != domain.TreehouseLeaseActive || attempt.TreehouseLeaseID != lease.LeaseID ||
		attempt.TreehouseLeaseHolder != lease.LeaseHolder || attempt.WorktreePath != lease.WorktreePath ||
		attempt.BaseSHA != lease.BaseSHA {
		return domain.Task{}, db.ErrLeaseConflict
	}
	if task.State == domain.TaskReady && !strings.EqualFold(attempt.HeadSHA, in.HeadSHA) {
		return domain.Task{}, ErrHeadMismatch
	}
	if err := verifyLiveLease(ctx, c.Leases, lease); err != nil {
		return domain.Task{}, err
	}
	result, digest, resultPath, err := c.readResult(in, attempt)
	if err != nil {
		return domain.Task{}, err
	}
	completion, err := c.Git.VerifyCompletion(ctx, lease.WorktreePath, lease.BaseSHA)
	if err != nil {
		return domain.Task{}, err
	}
	if !strings.EqualFold(completion.HeadSHA, in.HeadSHA) {
		return domain.Task{}, ErrHeadMismatch
	}
	if completion.Branch != lease.Branch {
		return domain.Task{}, fmt.Errorf("completion branch %q does not match leased branch %q", completion.Branch, lease.Branch)
	}
	return c.Store.CompleteWorkerTask(ctx, in.CommandID, db.CompleteWorkerTaskInput{
		TaskID: in.TaskID, Attempt: in.Attempt, ExpectedVersion: expectedVersion,
		LeaseID: lease.LeaseID, LeaseHolder: lease.LeaseHolder, HeadSHA: completion.HeadSHA,
		ResultPath: resultPath, ResultSHA256: digest, Result: result, Actor: "worker",
	})
}

func verifyLiveLease(ctx context.Context, observer LeaseObserver, lease domain.TreehouseLease) error {
	statuses, err := observer.Status(ctx, lease.ProjectPath)
	if err != nil {
		return fmt.Errorf("observe Treehouse lease: %w", err)
	}
	for _, status := range statuses {
		if status.WorktreePath != lease.WorktreePath {
			continue
		}
		if status.Status == "leased" && status.LeaseID == lease.LeaseID && status.LeaseHolder == lease.LeaseHolder {
			return nil
		}
		return db.ErrLeaseConflict
	}
	return db.ErrLeaseConflict
}

func (c *Completer) readResult(in CompleteRequest, attempt domain.TaskAttempt) (domain.WorkerResult, string, string, error) {
	var zero domain.WorkerResult
	expectedDir, err := c.TaskFiles.AttemptDir(in.TaskID, in.Attempt)
	if err != nil {
		return zero, "", "", err
	}
	expected := filepath.Join(expectedDir, "result.json")
	actual, err := filepath.Abs(in.ResultPath)
	if err != nil {
		return zero, "", "", fmt.Errorf("resolve result path: %w", err)
	}
	expected, err = filepath.Abs(expected)
	if err != nil {
		return zero, "", "", fmt.Errorf("resolve expected result path: %w", err)
	}
	if filepath.Clean(actual) != filepath.Clean(expected) {
		return zero, "", "", fmt.Errorf("%w: result must be the current attempt artifact %s", ErrStaleResult, expected)
	}
	info, err := os.Lstat(actual)
	if err != nil {
		return zero, "", "", fmt.Errorf("read worker result metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return zero, "", "", fmt.Errorf("%w: result must be a regular file no larger than 1 MiB", ErrInvalidResult)
	}
	if attempt.StartedAt == nil || !info.ModTime().After(*attempt.StartedAt) {
		return zero, "", "", fmt.Errorf("%w: result predates the current attempt", ErrStaleResult)
	}
	data, err := os.ReadFile(actual)
	if err != nil {
		return zero, "", "", fmt.Errorf("read worker result: %w", err)
	}
	result, err := decodeResult(data)
	if err != nil {
		return zero, "", "", err
	}
	digest := sha256.Sum256(data)
	return result, hex.EncodeToString(digest[:]), actual, nil
}

func decodeResult(data []byte) (domain.WorkerResult, error) {
	var result domain.WorkerResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("%w: decode JSON: %v", ErrInvalidResult, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return result, err
	}
	if result.Version != 1 || result.Status != "completed" || strings.TrimSpace(result.Summary) == "" ||
		result.Verification == nil || len(result.Verification) == 0 || result.ChangedFiles == nil ||
		len(result.ChangedFiles) == 0 || result.Risks == nil {
		return result, fmt.Errorf("%w: version, completed status, summary, verification, changed_files, and risks are required", ErrInvalidResult)
	}
	for _, check := range result.Verification {
		if strings.TrimSpace(check.Command) == "" || check.ExitCode != 0 {
			return result, fmt.Errorf("%w: verification entries require a command and zero exit code", ErrInvalidResult)
		}
	}
	seen := make(map[string]struct{}, len(result.ChangedFiles))
	for _, changed := range result.ChangedFiles {
		clean := filepath.Clean(changed)
		if changed == "" || filepath.IsAbs(changed) || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean != changed {
			return result, fmt.Errorf("%w: changed_files contains unsafe path %q", ErrInvalidResult, changed)
		}
		if _, exists := seen[changed]; exists {
			return result, fmt.Errorf("%w: duplicate changed file %q", ErrInvalidResult, changed)
		}
		seen[changed] = struct{}{}
	}
	return result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidResult)
		}
		return fmt.Errorf("%w: trailing content: %v", ErrInvalidResult, err)
	}
	return nil
}
