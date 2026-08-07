package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonLifecycleUsesRecordedPIDOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := currentDaemonPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := daemonStatus(paths); exitCode(err) != daemonNotRunningExitCode {
		t.Fatalf("initial status = %v", err)
	}
	binary := filepath.Join(t.TempDir(), "fake-pintellectd")
	writeCLIFile(t, binary, `#!/bin/sh
set -eu
health=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--status-file" ]; then health="$2"; shift 2; continue; fi
  shift
done
printf '%s\n' '{"version":"test","started_at":"2026-01-01T00:00:00Z","last_reconciled_at":"2026-01-01T00:00:01Z","database_path":"/tmp/test.db"}' > "$health"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`, 0o700)
	t.Cleanup(func() { _ = daemonStop(paths) })
	if err := daemonStart(paths, filepath.Join(t.TempDir(), "state.db"), binary); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.pid); err != nil {
		t.Fatalf("pidfile: %v", err)
	}
	if err := daemonStart(paths, "", binary); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("double start error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(paths.health); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not write health")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := daemonStatus(paths); err != nil {
		t.Fatal(err)
	}
	if err := daemonStop(paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.pid); !os.IsNotExist(err) {
		t.Fatalf("pidfile after stop: %v", err)
	}
	if err := daemonStop(paths); exitCode(err) != daemonNotRunningExitCode {
		t.Fatalf("second stop = %v", err)
	}
	if err := daemonStart(paths, "", binary); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemonCommand(context.Background(), []string{"restart", "--daemon-binary", binary}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(paths.pid)
	if err != nil || string(before) == string(after) {
		t.Fatalf("restart pidfile before=%q after=%q err=%v", before, after, err)
	}
	if err := daemonStop(paths); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), []string{"daemon", "stop"}); exitCode(err) != daemonNotRunningExitCode {
		t.Fatalf("unrecorded stop = %v", err)
	}
}
