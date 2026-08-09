package flow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/store"
	"sophon/internal/treehouse"
)

// PublishResult is the `sophon worker complete` core: it validates the
// worker's result hard, pins the claimed head to the live worktree HEAD, and
// atomically publishes the result bytes into the worker's own attempt dir.
// The shared mutation lock serializes completion against retry and typed report
// publication so conflicting evidence can never win by timestamp.
// It returns the SHA-256 of the published bytes.
func (f *Flow) PublishResult(ctx context.Context, taskID string, attempt int, headSHA, resultPath string) (string, error) {
	if f.deps.Git == nil {
		return "", errors.New("flow is not fully configured for worker completion")
	}
	if err := requireNonEmpty(taskID, headSHA, resultPath); err != nil || attempt < 1 {
		return "", errors.New("task, attempt, head SHA, and result path are required")
	}
	release, err := store.Acquire(ctx, "worker complete "+taskID)
	if err != nil {
		return "", err
	}
	defer release()
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
	data, err := readGuardedSubmission(resultPath, attemptDir, store.CompletionSubmissionName, spawn.StartedAt, ErrInvalidResult)
	if err != nil {
		return "", err
	}
	if _, err := store.DecodeWorkerResult(data); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}
	snapshot, err := f.deps.Git.Snapshot(ctx, spawn.WorktreePath)
	if err != nil {
		return "", fmt.Errorf("snapshot attempt worktree: %w", err)
	}
	if !strings.EqualFold(snapshot.Head, headSHA) {
		return "", fmt.Errorf("%w: worktree HEAD is %s", ErrHeadMismatch, snapshot.Head)
	}
	digest, _, err := publishEvidence(attemptDir, "result.json", "report.json", data)
	if err != nil {
		return "", fmt.Errorf("publish worker result: %w", err)
	}
	state := "ready"
	if spawn.Revision > 1 {
		state = "correction-ready"
	}
	store.AppendWake(taskID, fmt.Sprintf("%s: result published (attempt %d)", state, attempt))
	return digest, nil
}

// PublishReport is the `sophon worker report` core. It validates typed
// non-completion evidence, exact task/attempt/head identity, and the live
// worktree head before atomically publishing report.json. Dirty work is
// allowed and preserved; a report is attention evidence, never completion.
func (f *Flow) PublishReport(ctx context.Context, taskID string, attempt int, headSHA, reportPath string) (string, error) {
	if f.deps.Git == nil {
		return "", errors.New("flow is not fully configured for worker report")
	}
	if err := requireNonEmpty(taskID, headSHA, reportPath); err != nil || attempt < 1 {
		return "", errors.New("task, attempt, head SHA, and report path are required")
	}
	release, err := store.Acquire(ctx, "worker report "+taskID)
	if err != nil {
		return "", err
	}
	defer release()
	task, err := store.FindTask(taskID)
	if err != nil {
		return "", err
	}
	spawn, err := store.ReadSpawn(task.MissionID, taskID, attempt)
	if err != nil {
		return "", fmt.Errorf("publish report for attempt %d: %w", attempt, err)
	}
	homeDir, err := datahome.Dir()
	if err != nil {
		return "", err
	}
	attemptDir := store.AttemptDir(homeDir, task.MissionID, taskID, attempt)
	data, err := readGuardedSubmission(reportPath, attemptDir, store.ReportSubmissionName, spawn.StartedAt, ErrInvalidReport)
	if err != nil {
		return "", err
	}
	report, err := store.DecodeWorkerReport(data)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidReport, err)
	}
	if report.TaskID != taskID || report.Attempt != attempt || !strings.EqualFold(report.HeadSHA, headSHA) {
		return "", fmt.Errorf("%w: report identity %s attempt %d head %s does not match command identity",
			ErrInvalidReport, report.TaskID, report.Attempt, report.HeadSHA)
	}
	snapshot, err := f.deps.Git.Snapshot(ctx, spawn.WorktreePath)
	if err != nil {
		return "", fmt.Errorf("snapshot attempt worktree: %w", err)
	}
	if !strings.EqualFold(snapshot.Head, headSHA) {
		return "", fmt.Errorf("%w: worktree HEAD is %s", ErrHeadMismatch, snapshot.Head)
	}
	digest, _, err := publishEvidence(attemptDir, "report.json", "result.json", data)
	if err != nil {
		return "", fmt.Errorf("publish worker report: %w", err)
	}
	store.AppendWake(taskID, fmt.Sprintf("attention: %s report published (attempt %d)", report.Status, attempt))
	return digest, nil
}

// readGuardedSubmission enforces the worker submission guards: the path must
// be the exact generated staging filename (never canonical truth), a regular
// file no larger than 1 MiB, and written after the attempt started.
func readGuardedSubmission(submissionPath, attemptDir, expectedName string, startedAt time.Time, invalid error) ([]byte, error) {
	expected := filepath.Join(attemptDir, expectedName)
	resolved, err := filepath.Abs(submissionPath)
	if err != nil {
		return nil, fmt.Errorf("resolve submission path: %w", err)
	}
	expected, err = filepath.Abs(expected)
	if err != nil {
		return nil, fmt.Errorf("resolve expected submission path: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(expected) {
		return nil, fmt.Errorf("%w: submission must use generated staging path %s", invalid, expected)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve submission path: %w", err)
	}
	expected, err = filepath.EvalSymlinks(expected)
	if err != nil {
		return nil, fmt.Errorf("resolve expected submission path: %w", err)
	}
	if resolved != expected {
		return nil, fmt.Errorf("%w: submission staging path cannot redirect through a symlink", invalid)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("read worker submission metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("%w: submission must be a regular file no larger than 1 MiB", invalid)
	}
	if !info.ModTime().After(startedAt) {
		return nil, fmt.Errorf("%w: submission predates the attempt start", invalid)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read worker submission: %w", err)
	}
	return data, nil
}

// publishEvidence converges identical retries and refuses both differing
// same-kind evidence and report-vs-completion conflicts. Callers hold the
// shared mutation lock, so the sibling check and atomic rename are serialized.
func publishEvidence(attemptDir, canonicalName, siblingName string, data []byte) (digest string, published bool, err error) {
	digestBytes := sha256.Sum256(data)
	digest = hex.EncodeToString(digestBytes[:])
	if _, err := os.Stat(filepath.Join(attemptDir, siblingName)); err == nil {
		return "", false, fmt.Errorf("%w: %s already exists", ErrEvidenceConflict, siblingName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect conflicting evidence: %w", err)
	}
	canonical := filepath.Join(attemptDir, canonicalName)
	if existing, err := os.ReadFile(canonical); err == nil {
		if bytes.Equal(existing, data) {
			return digest, false, nil
		}
		return "", false, fmt.Errorf("%w: %s already contains different evidence", ErrEvidenceConflict, canonicalName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read existing evidence: %w", err)
	}
	if err := store.PublishBytes(canonical, data); err != nil {
		return "", false, err
	}
	return digest, true, nil
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
	mission, err = f.resolveMissionProject(ctx, mission)
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
	if existing, err := store.ReadOutcome(task.MissionID, taskID, attempt); err == nil {
		if existing.TaskID != taskID || existing.Attempt != attempt ||
			(existing.Revision != 0 && existing.Revision != spawn.Revision) {
			return store.Outcome{}, fmt.Errorf("%w: existing outcome identity does not match current revision", ErrEvidenceConflict)
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Outcome{}, err
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
	if _, err := store.DecodeWorkerResult(resultBytes); err != nil {
		return store.Outcome{}, fmt.Errorf("%w: canonical result: %v", ErrInvalidResult, err)
	}
	if _, err := os.Stat(store.AttemptPath(homeDir, task.MissionID, taskID, attempt, "report.json")); err == nil {
		return store.Outcome{}, fmt.Errorf("%w: current attempt contains both result.json and report.json", ErrEvidenceConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return store.Outcome{}, fmt.Errorf("inspect current attempt report: %w", err)
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
	outcome := store.Outcome{TaskID: taskID, Attempt: attempt, Revision: spawn.Revision, HeadSHA: completion.HeadSHA,
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
