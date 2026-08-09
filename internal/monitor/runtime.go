package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sophon/internal/store"
)

const runtimeVersion = 1

type RuntimeRecord struct {
	Version    int       `json:"version"`
	Generation string    `json:"generation"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
}

func RuntimeDir(home string) string  { return filepath.Join(store.StateDir(home), "monitor") }
func SocketPath(home string) string  { return filepath.Join(RuntimeDir(home), "rpc.sock") }
func RuntimePath(home string) string { return filepath.Join(RuntimeDir(home), "runtime.json") }
func LockPath(home string) string    { return filepath.Join(RuntimeDir(home), "start.lock") }
func LogPath(home string) string     { return filepath.Join(RuntimeDir(home), "monitor.log") }

func socketAlias(home string) string {
	return filepath.Join("/tmp", "sophon-monitor-"+digestBytes([]byte(home))[:32])
}

// socketAddress returns the pathname passed to the kernel. The socket inode
// always lives at SocketPath under the private data home. When that absolute
// pathname exceeds Unix sockaddr limits, a deterministic user-specific /tmp
// symlink provides only a short addressing path to the same private parent.
// The link is never trusted unless it resolves to the exact runtime directory.
func socketAddress(home string) (string, error) {
	actual := SocketPath(home)
	if len(actual) < 104 {
		return actual, nil
	}
	alias := socketAlias(home)
	if target, err := os.Readlink(alias); err == nil {
		if target != RuntimeDir(home) {
			return "", errors.New("long-path monitor socket alias points to another data home")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		temporary := alias + "." + fmt.Sprint(os.Getpid())
		_ = os.Remove(temporary)
		if err := os.Symlink(RuntimeDir(home), temporary); err != nil {
			return "", fmt.Errorf("create long-path monitor socket alias: %w", err)
		}
		if err := os.Rename(temporary, alias); err != nil {
			_ = os.Remove(temporary)
			if target, readErr := os.Readlink(alias); readErr != nil || target != RuntimeDir(home) {
				return "", fmt.Errorf("publish long-path monitor socket alias: %w", err)
			}
		}
	} else {
		return "", fmt.Errorf("inspect long-path monitor socket alias: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	canonicalRuntime, runtimeErr := filepath.EvalSymlinks(RuntimeDir(home))
	if err != nil || runtimeErr != nil || strings.TrimSuffix(resolved, string(filepath.Separator)) != strings.TrimSuffix(canonicalRuntime, string(filepath.Separator)) {
		return "", errors.New("long-path monitor socket alias does not resolve to the exact private runtime directory")
	}
	return filepath.Join(alias, "rpc.sock"), nil
}

func cleanupSocketAlias(home string) {
	if len(SocketPath(home)) < 104 {
		return
	}
	alias := socketAlias(home)
	if target, err := os.Readlink(alias); err == nil && target == RuntimeDir(home) {
		_ = os.Remove(alias)
	}
}

func EnsureRuntimeDir(home string) error {
	if !filepath.IsAbs(home) {
		return errors.New("monitor data home must be absolute")
	}
	dir := RuntimeDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create monitor runtime directory: %w", err)
	}
	for _, protected := range []string{home, store.StateDir(home), dir} {
		info, err := os.Lstat(protected)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("monitor path must be a real directory, not a symlink: %s", protected)
		}
		if info.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(protected, 0o700); err != nil {
				return fmt.Errorf("protect monitor directory %s: %w", protected, err)
			}
		}
	}
	_, err := socketAddress(home)
	return err
}

func readRuntime(home string) (RuntimeRecord, error) {
	var record RuntimeRecord
	path := RuntimePath(home)
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return record, errors.New("monitor runtime record is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return record, errors.New("monitor runtime record is not user-private")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := decodeStrict(data, &record); err != nil {
		return record, fmt.Errorf("decode monitor runtime record: %w", err)
	}
	if record.Version != runtimeVersion || !safeGeneration.MatchString(record.Generation) || record.PID <= 0 || record.StartedAt.IsZero() {
		return RuntimeRecord{}, errors.New("monitor runtime record has invalid identity")
	}
	return record, nil
}

func publishRuntime(home string, record RuntimeRecord) error {
	return store.Publish(RuntimePath(home), record)
}

func withStartLock(home string, action func() error) error {
	if info, err := os.Lstat(LockPath(home)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("monitor start lock path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect monitor start lock: %w", err)
	}
	file, err := os.OpenFile(LockPath(home), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open monitor start lock: %w", err)
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("monitor start lock is not a user-private regular file")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock monitor startup: %w", err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return action()
}

func processDefinitelyDead(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	default:
		return false, fmt.Errorf("probe monitor pid %d: %w", pid, err)
	}
}

func PublicState(home string) PublicStatus {
	status := PublicStatus{ProtocolVersion: ProtocolVersion, Status: "stopped"}
	record, err := readRuntime(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, socketErr := os.Lstat(SocketPath(home)); socketErr == nil {
				status.Status = "unavailable"
				status.Detail = "orphan monitor socket exists without a runtime identity"
			} else if !errors.Is(socketErr, os.ErrNotExist) {
				status.Status = "unavailable"
				status.Detail = socketErr.Error()
			}
		} else {
			status.Status = "unavailable"
			status.Detail = err.Error()
		}
		return status
	}
	status.PID = record.PID
	status.StartedAt = record.StartedAt.Format(time.RFC3339Nano)
	client := NewClient(home)
	ping, err := client.Ping()
	if err != nil {
		status.Status = "unavailable"
		status.Detail = err.Error()
		return status
	}
	status.Running = true
	status.Status = "running"
	status.Capabilities = ping.Capabilities
	return status
}

func MarshalPublicState(home string) ([]byte, error) {
	return json.Marshal(PublicState(home))
}
