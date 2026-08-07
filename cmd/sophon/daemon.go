package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sophon/internal/datahome"
)

const daemonNotRunningExitCode = 3

type daemonPaths struct{ pid, log, health string }

type daemonHealth struct {
	Version          string    `json:"version"`
	StartedAt        time.Time `json:"started_at"`
	LastReconciledAt time.Time `json:"last_reconciled_at"`
	DatabasePath     string    `json:"database_path"`
}

func daemonCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected: sophon daemon status|start|stop|restart")
	}
	flags := flag.NewFlagSet("daemon "+args[0], flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	daemonBinary := flags.String("daemon-binary", "", "sophond binary path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("daemon %s does not accept positional arguments", args[0])
	}
	paths, err := currentDaemonPaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		return daemonStatus(paths)
	case "start":
		return daemonStart(paths, *dbPath, *daemonBinary)
	case "stop":
		return daemonStop(paths)
	case "restart":
		if err := daemonStop(paths); err != nil {
			var status *exitError
			if !errors.As(err, &status) || status.code != daemonNotRunningExitCode {
				return err
			}
		}
		return daemonStart(paths, *dbPath, *daemonBinary)
	default:
		return errors.New("expected: sophon daemon status|start|stop|restart")
	}
}

func currentDaemonPaths() (daemonPaths, error) {
	location, err := datahome.Resolve()
	if err != nil {
		return daemonPaths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return daemonPaths{pid: location.DaemonPIDPath(), log: location.DaemonLogPath(), health: location.DaemonHealthPath()}, nil
}

func daemonPID(paths daemonPaths) (int, bool) {
	content, err := os.ReadFile(paths.pid)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, processRunning(pid)
}

func processRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func daemonStatus(paths daemonPaths) error {
	pid, running := daemonPID(paths)
	if !running {
		fmt.Println("Daemon: not running")
		return &exitError{code: daemonNotRunningExitCode, err: errors.New("daemon is not running")}
	}
	health, err := readDaemonHealth(paths.health)
	if err != nil {
		return err
	}
	fmt.Printf("Daemon: running\nPID: %d\nVersion: %s\nUptime: %s\nLast successful reconcile: %s\nDatabase: %s\n", pid, health.Version, time.Since(health.StartedAt).Round(time.Second), health.LastReconciledAt.Format(time.RFC3339), health.DatabasePath)
	return nil
}

func daemonStart(paths daemonPaths, dbPath, binary string) error {
	if pid, running := daemonPID(paths); running {
		return fmt.Errorf("daemon already running (pid %d recorded in %s)", pid, paths.pid)
	}
	if err := os.MkdirAll(filepath.Dir(paths.pid), 0o700); err != nil {
		return fmt.Errorf("create daemon directory: %w", err)
	}
	if binary == "" {
		var err error
		binary, err = findDaemonBinary()
		if err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(paths.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	args := []string{"--status-file", paths.health}
	if dbPath != "" {
		args = append(args, "--db", dbPath)
	}
	command := exec.Command(binary, args...)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	if err := os.WriteFile(paths.pid, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o600); err != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("write daemon pidfile: %w", err)
	}
	go func() { _ = command.Wait() }()
	fmt.Printf("Daemon started (pid %d; pidfile %s)\n", command.Process.Pid, paths.pid)
	return nil
}

func findDaemonBinary() (string, error) {
	self, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(self), "sophond")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if candidate, err := exec.LookPath("sophond"); err == nil {
		return candidate, nil
	}
	return "", errors.New("sophond is not installed beside sophon or on PATH")
}

func daemonStop(paths daemonPaths) error {
	content, err := os.ReadFile(paths.pid)
	if errors.Is(err, os.ErrNotExist) {
		return &exitError{code: daemonNotRunningExitCode, err: errors.New("daemon is not running")}
	}
	if err != nil {
		return fmt.Errorf("read daemon pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("refusing to stop daemon: invalid pidfile %s", paths.pid)
	}
	if !processRunning(pid) {
		if err := os.Remove(paths.pid); err != nil {
			return fmt.Errorf("clear stale daemon pidfile: %w", err)
		}
		fmt.Println("Daemon was not running; cleared stale pidfile.")
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop daemon pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processRunning(pid) {
		return fmt.Errorf("daemon pid %d did not exit after SIGTERM", pid)
	}
	if err := os.Remove(paths.pid); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear daemon pidfile: %w", err)
	}
	fmt.Printf("Daemon stopped (pid %d)\n", pid)
	return nil
}

func readDaemonHealth(path string) (daemonHealth, error) {
	file, err := os.Open(path)
	if err != nil {
		return daemonHealth{}, fmt.Errorf("read daemon health: %w", err)
	}
	defer file.Close()
	var health daemonHealth
	if err := json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&health); err != nil {
		return daemonHealth{}, fmt.Errorf("decode daemon health: %w", err)
	}
	return health, nil
}
