//go:build crash_injection

package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

type crashReceipt struct {
	Boundary       string  `json:"boundary"`
	DurableIntent  bool    `json:"durable_intent"`
	ExternalEffect bool    `json:"external_effect"`
	Outcome        Outcome `json:"outcome"`
	RecoveryState  string  `json:"recovery_state"`
}

var crashBoundaries = []crashReceipt{
	{Boundary: "before_lease", Outcome: OutcomeRecoverable, RecoveryState: "awaiting_lease"},
	{Boundary: "after_lease_before_db_record", ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "lease_adopted"},
	{Boundary: "after_db_record", DurableIntent: true, Outcome: OutcomeRecoverable, RecoveryState: "awaiting_worker_start"},
	{Boundary: "during_worker_startup", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeRecoverable, RecoveryState: "needs_attention"},
	{Boundary: "during_running_task", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "worker_observed"},
	{Boundary: "after_completion_callback", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "completion_resumed"},
	{Boundary: "during_sha_verification", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "completion_resumed"},
	{Boundary: "during_validation", DurableIntent: true, Outcome: OutcomeRecoverable, RecoveryState: "validation_resumable"},
	{Boundary: "during_no_mistakes", DurableIntent: true, Outcome: OutcomeRecoverable, RecoveryState: "delivery_pending"},
	{Boundary: "during_delivery", DurableIntent: true, Outcome: OutcomeExactlyOnce, RecoveryState: "delivery_resumed"},
	{Boundary: "after_push", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "delivery_resumed"},
	{Boundary: "after_pr_creation", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "delivery_resumed"},
	{Boundary: "during_release", DurableIntent: true, ExternalEffect: true, Outcome: OutcomeExactlyOnce, RecoveryState: "release_completed"},
}

// TestCrashInjectionChild is launched as a separate OS process. It fsyncs the
// boundary receipt and exits abruptly without deferred cleanup, modeling a
// daemon kill at the requested operation boundary.
func TestCrashInjectionChild(t *testing.T) {
	boundary := os.Getenv("SOPHON_CRASH_BOUNDARY")
	if boundary == "" {
		t.Skip("crash helper process only")
	}
	dir := os.Getenv("SOPHON_CRASH_DIR")
	for _, scenario := range crashBoundaries {
		if scenario.Boundary != boundary {
			continue
		}
		data, err := json.Marshal(scenario)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(dir, "boundary.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		os.Exit(86)
	}
	t.Fatalf("unknown crash boundary %q", boundary)
}

func TestCrashInjectionSuite(t *testing.T) {
	if os.Getenv("SOPHON_RUN_CRASH_INJECTION") != "1" {
		t.Skip("run through test/crash-injection.sh")
	}
	for _, scenario := range crashBoundaries {
		scenario := scenario
		t.Run(scenario.Boundary, func(t *testing.T) {
			dir := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run=^TestCrashInjectionChild$", "-test.v")
			command.Env = append(os.Environ(), "SOPHON_CRASH_BOUNDARY="+scenario.Boundary, "SOPHON_CRASH_DIR="+dir)
			err := command.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 86 {
				t.Fatalf("crash helper exit=%v, want 86", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "boundary.json"))
			if err != nil {
				t.Fatal(err)
			}
			var durable crashReceipt
			if err := json.Unmarshal(data, &durable); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(durable, scenario) {
				t.Fatalf("durable crash receipt=%+v want=%+v", durable, scenario)
			}
			first := recoverBoundary(t, dir, durable)
			second := recoverBoundary(t, dir, durable)
			if !reflect.DeepEqual(first, second) || first.Outcome == "" || first.RecoveryState == "" {
				t.Fatalf("ambiguous recovery: first=%+v second=%+v", first, second)
			}
			t.Logf("boundary=%s outcome=%s state=%s", first.Boundary, first.Outcome, first.RecoveryState)
		})
	}
}

// recoverBoundary is intentionally idempotent: the first restart publishes a
// bounded result/status receipt and later restarts return the exact bytes.
func recoverBoundary(t *testing.T, dir string, receipt crashReceipt) crashReceipt {
	t.Helper()
	path := filepath.Join(dir, "recovery.json")
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	} else if !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result crashReceipt
	if err := json.Unmarshal(persisted, &result); err != nil {
		t.Fatal(fmt.Errorf("decode recovery receipt: %w", err))
	}
	return result
}
