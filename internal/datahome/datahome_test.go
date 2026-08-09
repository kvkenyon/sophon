package datahome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataHomeOverrideWins(t *testing.T) {
	override := t.TempDir()
	t.Setenv(OverrideEnv, override)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != override {
		t.Fatalf("Dir = %q, want override %q", dir, override)
	}
}

func TestDirWithoutOverrideUsesHome(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".sophon") {
		t.Fatalf("Dir = %q, want %q", dir, filepath.Join(home, ".sophon"))
	}
}

func TestDirIgnoresLegacyDirectory(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dir, ".parallel-intellect") {
		t.Fatalf("Dir = %q, legacy fallback must be gone", dir)
	}
	if !strings.HasPrefix(dir, home) {
		t.Fatalf("Dir = %q, want under home %q", dir, home)
	}
}

func TestAbsDirResolvesCleanAbsolutePath(t *testing.T) {
	base := t.TempDir()
	t.Setenv(OverrideEnv, filepath.Join(base, "smoke home", "..", "smoke home"))
	dir, err := AbsDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "smoke home"); dir != want {
		t.Fatalf("AbsDir = %q, want %q", dir, want)
	}
}
