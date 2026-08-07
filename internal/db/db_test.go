package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := Open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := os.Stat(filepath.Join(home, ".sophon", "sophon.db")); err != nil {
		t.Fatalf("default database was not created: %v", err)
	}
}

func TestOpenDefaultPathUsesLegacyDatabase(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".parallel-intellect")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	store, err := Open(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := os.Stat(filepath.Join(legacy, "pintellect.db")); err != nil {
		t.Fatalf("legacy database was not opened: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".sophon")); !os.IsNotExist(err) {
		t.Fatalf("new data directory should remain absent, got %v", err)
	}
}
