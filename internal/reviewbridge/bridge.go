// Package reviewbridge owns the volatile single-process lease for one exact
// task/attempt/revision bridge. The kernel lock is authority; the JSON owner
// file is inspectable routing metadata only, so stale PIDs and PID reuse can
// never authorize cleanup or another target.
package reviewbridge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"sophon/internal/store"
)

var (
	safeTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	hex24      = regexp.MustCompile(`^[0-9a-f]{24}$`)
	hex40      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Owner struct {
	Version        int       `json:"version"`
	TaskID         string    `json:"task_id"`
	Attempt        int       `json:"attempt"`
	SessionID      string    `json:"session_id"`
	BaseSHA        string    `json:"base_sha"`
	HeadSHA        string    `json:"head_sha"`
	DataHomeDigest string    `json:"data_home_digest"`
	PID            int       `json:"pid"`
	Nonce          string    `json:"nonce"`
	StartedAt      time.Time `json:"started_at"`
}

type Lease struct {
	file  *os.File
	path  string
	owner Owner
}

func Expected(home string, binding store.ReviewBinding) Owner {
	digest := sha256.Sum256([]byte(filepath.Clean(home)))
	return Owner{Version: 1, TaskID: binding.TaskID, Attempt: binding.Attempt, SessionID: binding.SessionID,
		BaseSHA: binding.BaseSHA, HeadSHA: binding.HeadSHA, DataHomeDigest: hex.EncodeToString(digest[:])}
}

func RuntimeDir(home string) string { return filepath.Join(store.StateDir(home), "review-bridges") }

func LockPath(home, taskID string, attempt int) string {
	return filepath.Join(RuntimeDir(home), taskID+"-"+strconv.Itoa(attempt)+".lock")
}

func OwnerPath(home, taskID string, attempt int) string {
	return filepath.Join(RuntimeDir(home), taskID+"-"+strconv.Itoa(attempt)+".json")
}

func Acquire(home string, expected Owner) (*Lease, bool, error) {
	if err := validateExpectedForHome(home, expected); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(RuntimeDir(home), 0o700); err != nil {
		return nil, false, fmt.Errorf("create review bridge runtime: %w", err)
	}
	if info, err := os.Lstat(RuntimeDir(home)); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("review bridge runtime is not a real directory")
	}
	lockPath := LockPath(home, expected.TaskID, expected.Attempt)
	file, err := openLock(lockPath)
	if err != nil {
		return nil, false, fmt.Errorf("open review bridge lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock review bridge: %w", err)
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		return nil, false, err
	}
	expected.PID = os.Getpid()
	expected.Nonce = hex.EncodeToString(nonceBytes)
	expected.StartedAt = time.Now().UTC()
	ownerPath := OwnerPath(home, expected.TaskID, expected.Attempt)
	if err := store.Publish(ownerPath, expected); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		return nil, false, err
	}
	return &Lease{file: file, path: ownerPath, owner: expected}, true, nil
}

func (l *Lease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	// Remove only our exact nonce-bearing observation. A successor can never
	// own the lock until after unlock, so this cannot erase another owner.
	if owner, err := readOwner(l.path); err == nil && owner.Nonce == l.owner.Nonce {
		_ = os.Remove(l.path)
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

// Running reports whether an exact owner currently holds the kernel lock.
// PID is never consulted, so a stale record or reused PID cannot claim life.
func Running(home string, expected Owner) (bool, error) {
	if err := validateExpectedForHome(home, expected); err != nil {
		return false, err
	}
	lockPath := LockPath(home, expected.TaskID, expected.Attempt)
	if info, err := os.Lstat(RuntimeDir(home)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("review bridge runtime is not a real directory")
	}
	if info, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("review bridge lock is not a regular file")
	}
	file, err := openExistingLock(lockPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return false, nil
	} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		return false, err
	}
	owner, err := readOwner(OwnerPath(home, expected.TaskID, expected.Attempt))
	if err != nil {
		return false, fmt.Errorf("review bridge lock is held without valid owner identity: %w", err)
	}
	return sameTarget(owner, expected), nil
}

func sameTarget(owner, expected Owner) bool {
	return owner.Version == expected.Version && owner.TaskID == expected.TaskID && owner.Attempt == expected.Attempt &&
		owner.SessionID == expected.SessionID && owner.BaseSHA == expected.BaseSHA && owner.HeadSHA == expected.HeadSHA &&
		owner.DataHomeDigest == expected.DataHomeDigest && owner.PID > 0 && len(owner.Nonce) == 32 && !owner.StartedAt.IsZero()
}

func validateExpected(owner Owner) error {
	if owner.Version != 1 || !safeTaskID.MatchString(owner.TaskID) || owner.Attempt < 1 ||
		!hex24.MatchString(owner.SessionID) || !hex40.MatchString(owner.BaseSHA) || !hex40.MatchString(owner.HeadSHA) ||
		!hex64.MatchString(owner.DataHomeDigest) || owner.BaseSHA == owner.HeadSHA {
		return errors.New("invalid exact review bridge identity")
	}
	return nil
}

func validateExpectedForHome(home string, owner Owner) error {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return errors.New("review bridge requires a clean absolute data home")
	}
	if err := validateExpected(owner); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(filepath.Clean(home)))
	if owner.DataHomeDigest != hex.EncodeToString(digest[:]) {
		return errors.New("review bridge identity belongs to a different data home")
	}
	return nil
}

func readOwner(path string) (Owner, error) {
	var owner Owner
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return owner, errors.New("review bridge owner is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return owner, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil {
		return Owner{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Owner{}, errors.New("review bridge owner has trailing content")
	}
	return owner, nil
}

func openLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	return checkedLockFile(fd, err, path)
}

func openExistingLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	return checkedLockFile(fd, err, path)
}

func checkedLockFile(fd int, openErr error, path string) (*os.File, error) {
	if openErr != nil {
		return nil, openErr
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("review bridge lock path is not a regular file")
	}
	return file, nil
}
