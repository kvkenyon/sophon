package flow

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
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	"sophon/internal/store"
	"sophon/internal/treehouse"
)

// PublishResult is the `sophon worker complete` core: it validates the
// worker's result hard, pins the claimed head to the live worktree HEAD, and
// atomically publishes the result bytes into the worker's own attempt dir.
// No shared lock is taken: a worker writes only its own attempt directory.
// It returns the SHA-256 of the published bytes.
func (f *Flow) PublishResult(ctx context.Context, taskID string, attempt int, headSHA, resultPath string) (string, error) {
	if f.deps.Git == nil {
		return "", errors.New("flow is not fully configured for worker completion")
	}
	if err := requireNonEmpty(taskID, headSHA, resultPath); err != nil || attempt < 1 {
		return "", errors.New("task, attempt, head SHA, and result path are required")
	}
	task, err := store.FindTask(taskID)
	if err != nil {
		return "", err
	}
	// A worker can only publish to an attempt that actually spawned.
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return "", fmt.Errorf("publish result for attempt %d: %w", attempt, err)
	}
	homeDir, err := datahome.Dir()
	if err != nil {
		return "", err
	}
	attemptDir := store.AttemptDir(homeDir, task.MissionID, taskID, attempt)
	data, err := readGuardedResult(resultPath, attemptDir, spawn.StartedAt)
	if err != nil {
		return "", err
	}
	if _, err := decodeResult(data); err != nil {
		return "", err
	}
	snapshot, err := f.deps.Git.Snapshot(ctx, spawn.WorktreePath)
	if err != nil {
		return "", fmt.Errorf("snapshot attempt worktree: %w", err)
	}
	if !strings.EqualFold(snapshot.Head, headSHA) {
		return "", fmt.Errorf("%w: worktree HEAD is %s", ErrHeadMismatch, snapshot.Head)
	}
	if err := store.PublishBytes(filepath.Join(attemptDir, "result.json"), data); err != nil {
		return "", fmt.Errorf("publish worker result: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("ready: result published (attempt %d)", attempt))
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// readGuardedResult enforces the worker result file guards: the resolved path
// must be a regular file inside the attempt directory, no larger than 1 MiB,
// and written after the attempt started.
func readGuardedResult(resultPath, attemptDir string, startedAt time.Time) ([]byte, error) {
	resolved, err := filepath.Abs(resultPath)
	if err != nil {
		return nil, fmt.Errorf("resolve result path: %w", err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve result path: %w", err)
	}
	dir, err := filepath.EvalSymlinks(attemptDir)
	if err != nil {
		return nil, fmt.Errorf("resolve attempt directory: %w", err)
	}
	relative, err := filepath.Rel(dir, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: result must live inside the attempt directory %s", ErrInvalidResult, dir)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("read worker result metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("%w: result must be a regular file no larger than 1 MiB", ErrInvalidResult)
	}
	if !info.ModTime().After(startedAt) {
		return nil, fmt.Errorf("%w: result predates the attempt start", ErrInvalidResult)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read worker result: %w", err)
	}
	return data, nil
}

// decodeResult enforces the strict version 1 worker completion schema.
func decodeResult(data []byte) (domain.WorkerResult, error) {
	var result domain.WorkerResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("%w: decode JSON: %v", ErrInvalidResult, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return result, fmt.Errorf("%w: trailing JSON value", ErrInvalidResult)
		}
		return result, fmt.Errorf("%w: trailing content: %v", ErrInvalidResult, err)
	}
	if result.Version != 1 || result.Status != "completed" || strings.TrimSpace(result.Summary) == "" ||
		len(result.Verification) == 0 || len(result.ChangedFiles) == 0 || result.Risks == nil {
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

// VerifyComplete proves the current attempt: spawn and result records exist,
// the lease is still live with exact identity, and Git shows a clean new
// descendant of the base SHA. It then publishes outcome.json. A result that
// exists only in a fenced attempt is refused with ErrStaleAttempt and nothing
// is mutated.
func (f *Flow) VerifyComplete(ctx context.Context, taskID string) (store.Outcome, error) {
	if f.deps.Git == nil || f.deps.Leases == nil {
		return store.Outcome{}, errors.New("flow is not fully configured for verification")
	}
	release, err := store.Acquire(ctx, "verify-complete "+taskID)
	if err != nil {
		return store.Outcome{}, err
	}
	defer release()
	task, mission, err := f.taskAndMission(taskID)
	if err != nil {
		return store.Outcome{}, err
	}
	attempt, err := currentAttempt(task)
	if err != nil {
		return store.Outcome{}, err
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return store.Outcome{}, fmt.Errorf("verify attempt %d: %w", attempt, err)
	}
	homeDir, err := datahome.Dir()
	if err != nil {
		return store.Outcome{}, err
	}
	resultBytes, err := os.ReadFile(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "result.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store.Outcome{}, f.notReadyOrStale(homeDir, task, attempt)
		}
		return store.Outcome{}, fmt.Errorf("read attempt result: %w", err)
	}
	if err := verifyLiveLease(ctx, f.deps.Leases, mission.ProjectPath, spawn); err != nil {
		return store.Outcome{}, err
	}
	completion, err := f.deps.Git.VerifyCompletion(ctx, spawn.WorktreePath, spawn.BaseSHA)
	if err != nil {
		return store.Outcome{}, err
	}
	if completion.Branch != spawn.Branch {
		return store.Outcome{}, fmt.Errorf("completion branch %q does not match spawned branch %q", completion.Branch, spawn.Branch)
	}
	digest := sha256.Sum256(resultBytes)
	outcome := store.Outcome{TaskID: taskID, Attempt: attempt, HeadSHA: completion.HeadSHA,
		Branch: completion.Branch, ResultSHA256: hex.EncodeToString(digest[:]), VerifiedAt: time.Now().UTC()}
	if err := store.Publish(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "outcome.json"), outcome); err != nil {
		return store.Outcome{}, fmt.Errorf("publish outcome: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("verified: attempt %d (head %s)", attempt, outcome.HeadSHA))
	return outcome, nil
}

// notReadyOrStale distinguishes a missing current-attempt result from a
// result stranded in a fenced attempt.
func (f *Flow) notReadyOrStale(homeDir string, task store.Task, attempt int) error {
	for older := attempt - 1; older >= 1; older-- {
		if _, err := os.Stat(store.AttemptPath(homeDir, task.MissionID, task.ID, older, "result.json")); err == nil {
			return fmt.Errorf("%w: attempt %d holds a result but the current attempt is %d; spawn --retry output is fenced",
				ErrStaleAttempt, older, attempt)
		}
	}
	return fmt.Errorf("%w: task %s attempt %d; the worker finishes via `sophon worker complete`",
		ErrNotReady, task.ID, attempt)
}

// verifyLiveLease requires the attempt worktree to appear in Treehouse status
// as leased with the exact recorded lease id and holder.
func verifyLiveLease(ctx context.Context, cli treehouse.CLI, projectPath string, spawn store.Spawn) error {
	statuses, err := cli.Status(ctx, projectPath)
	if err != nil {
		return fmt.Errorf("observe Treehouse lease: %w", err)
	}
	for _, status := range statuses {
		if status.WorktreePath != spawn.WorktreePath {
			continue
		}
		if status.Status == "leased" && status.LeaseID == spawn.LeaseID && status.LeaseHolder == spawn.LeaseHolder {
			return nil
		}
		return fmt.Errorf("%w: worktree %s is %s with lease %s/%s, want %s/%s",
			ErrLeaseConflict, spawn.WorktreePath, status.Status, status.LeaseID, status.LeaseHolder,
			spawn.LeaseID, spawn.LeaseHolder)
	}
	return fmt.Errorf("%w: worktree %s is not leased", ErrLeaseConflict, spawn.WorktreePath)
}
