package reviewbridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sophon/internal/store"
)

func testBinding() store.ReviewBinding {
	return store.ReviewBinding{Version: 1, Product: store.ReviewProduct, ProductSchemaVersion: 1,
		TaskID: "task_exact", Attempt: 2, SessionID: "57d91f3ddc544f34e70c1156",
		BaseSHA: "1111111111111111111111111111111111111111", HeadSHA: "2222222222222222222222222222222222222222",
		OpenedAt: time.Now().UTC()}
}

func TestKernelLockIsExactOwnerAndPIDReuseIsIrrelevant(t *testing.T) {
	home := t.TempDir()
	expected := Expected(home, testBinding())
	lease, acquired, err := Acquire(home, expected)
	if err != nil || !acquired {
		t.Fatalf("acquire = %t, %v", acquired, err)
	}
	defer lease.Release()
	if running, err := Running(home, expected); err != nil || !running {
		t.Fatalf("running = %t, %v", running, err)
	}
	if second, acquired, err := Acquire(home, expected); err != nil || acquired || second != nil {
		t.Fatalf("duplicate acquire = %+v, %t, %v", second, acquired, err)
	}
	// A PID-like stale record in another data home cannot claim this owner.
	otherHome := t.TempDir()
	stale := expected
	stale.PID = os.Getpid()
	stale.Nonce = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stale.StartedAt = time.Now().UTC()
	if err := os.MkdirAll(RuntimeDir(otherHome), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(OwnerPath(otherHome, stale.TaskID, stale.Attempt), stale); err != nil {
		t.Fatal(err)
	}
	if running, err := Running(otherHome, Expected(otherHome, testBinding())); err != nil || running {
		t.Fatalf("stale/reused PID claimed bridge: %t, %v", running, err)
	}
	if _, err := os.Lstat(LockPath(otherHome, stale.TaskID, stale.Attempt)); !os.IsNotExist(err) {
		t.Fatalf("read-only stale-owner observation created a lock file: %v", err)
	}
	if _, _, err := Acquire(otherHome, Expected(home, testBinding())); err == nil {
		t.Fatal("bridge identity from another data home was accepted")
	}
}

func TestBridgeRefusesUnsafeLockAndWrongHeldIdentity(t *testing.T) {
	home := t.TempDir()
	expected := Expected(home, testBinding())
	if err := os.MkdirAll(RuntimeDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, LockPath(home, expected.TaskID, expected.Attempt)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Acquire(home, expected); err == nil {
		t.Fatal("symlink lock target accepted")
	}

	home2 := t.TempDir()
	lease, acquired, err := Acquire(home2, Expected(home2, testBinding()))
	if err != nil || !acquired {
		t.Fatal(err)
	}
	defer lease.Release()
	wrong := testBinding()
	wrong.HeadSHA = "3333333333333333333333333333333333333333"
	if running, err := Running(home2, Expected(home2, wrong)); err != nil || running {
		t.Fatalf("wrong revision observed as running: %t, %v", running, err)
	}
}
