package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLocked marks a lock held by a live owner, or any ambiguous owner state.
// Acquisition never waits: callers get a descriptive refusal immediately.
var ErrLocked = errors.New("store lock is held")

// LockOwner records who holds the shared-mutation lock.
type LockOwner struct {
	PID        int       `json:"pid"`
	Command    string    `json:"command"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// Acquire takes the shared-mutation lock (state/.lock via atomic mkdir) and
// returns the release function. A lock whose owner pid is definitively dead
// is reclaimed once; a live owner or any ambiguity (unreadable owner record,
// EPERM probing the pid) fails conservatively with ErrLocked.
func Acquire(ctx context.Context, command string) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	homeDir, err := home()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(StateDir(homeDir), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	lockDir := LockDir(homeDir)
	for attempt := 0; attempt < 2; attempt++ {
		err := os.Mkdir(lockDir, 0o700)
		if err == nil {
			return publishOwner(lockDir, command)
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire store lock: %w", err)
		}
		if attempt == 1 {
			break
		}
		if err := reclaimStale(lockDir); err != nil {
			return nil, err
		}
	}
	owner, _ := readOwner(lockDir)
	return nil, fmt.Errorf("%w: command %q (pid %d, acquired %s)", ErrLocked,
		owner.Command, owner.PID, owner.AcquiredAt.Format(time.RFC3339))
}

// reclaimStale removes the lock only when its owner is definitively dead.
func reclaimStale(lockDir string) error {
	owner, err := readOwner(lockDir)
	if err != nil {
		return fmt.Errorf("%w: owner record is unreadable, refusing to reclaim: %v", ErrLocked, err)
	}
	if owner.PID <= 0 {
		return fmt.Errorf("%w: owner record has invalid pid %d, refusing to reclaim", ErrLocked, owner.PID)
	}
	err = syscall.Kill(owner.PID, 0)
	switch {
	case err == nil:
		return fmt.Errorf("%w: command %q (pid %d) is still alive", ErrLocked, owner.Command, owner.PID)
	case errors.Is(err, syscall.ESRCH):
		// The owner is definitively dead; reclaim and let Acquire retry once.
		if err := os.RemoveAll(lockDir); err != nil {
			return fmt.Errorf("reclaim dead store lock: %w", err)
		}
		return nil
	default:
		// EPERM or anything else: the pid may be alive. Fail conservatively.
		return fmt.Errorf("%w: cannot probe owner pid %d, refusing to reclaim: %v", ErrLocked, owner.PID, err)
	}
}

func publishOwner(lockDir, command string) (func(), error) {
	owner := LockOwner{PID: os.Getpid(), Command: command, AcquiredAt: time.Now().UTC()}
	if err := Publish(filepath.Join(lockDir, "owner.json"), owner); err != nil {
		os.RemoveAll(lockDir)
		return nil, err
	}
	return func() {
		os.Remove(filepath.Join(lockDir, "owner.json"))
		os.Remove(lockDir)
	}, nil
}

func readOwner(lockDir string) (LockOwner, error) {
	var owner LockOwner
	return owner, read(filepath.Join(lockDir, "owner.json"), &owner)
}
