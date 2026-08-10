package pi

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMaterializeUsesPrivateContentAddressedVerifiedAssets(t *testing.T) {
	home := t.TempDir()
	entry, err := Materialize(home)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Materialize(home)
	if err != nil {
		t.Fatal(err)
	}
	if again != entry {
		t.Fatalf("materialized entry changed: %q != %q", again, entry)
	}
	assets, identity, err := embeddedAssets()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "pi", "extensions", identity, "index.ts")
	if entry != want {
		t.Fatalf("entry = %q, want %q", entry, want)
	}
	if err := verifyMaterialized(filepath.Dir(entry), assets); err != nil {
		t.Fatalf("materialized asset verification: %v", err)
	}
	for _, name := range AssetNames() {
		info, err := os.Stat(filepath.Join(filepath.Dir(entry), filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestMaterializeRefusesModifiedPartialAndSubstitutedAssets(t *testing.T) {
	t.Run("modified", func(t *testing.T) {
		home := t.TempDir()
		entry, err := Materialize(home)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry, []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Materialize(home); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("partial", func(t *testing.T) {
		home := t.TempDir()
		_, identity, err := embeddedAssets()
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, "pi", "extensions", identity)
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(home, "pi"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(home, "pi", "extensions"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Materialize(home); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(home, "pi")); err != nil {
			t.Fatal(err)
		}
		if _, err := Materialize(home); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestMaterializeConcurrentlyReusesOneExactVersion(t *testing.T) {
	home := t.TempDir()
	const workers = 12
	entries := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			entry, err := Materialize(home)
			if err != nil {
				errs <- err
				return
			}
			entries <- entry
		}()
	}
	group.Wait()
	close(entries)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first string
	for entry := range entries {
		if first == "" {
			first = entry
		} else if entry != first {
			t.Fatalf("entry = %q, want %q", entry, first)
		}
	}
}
