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

	if _, err := os.Stat(filepath.Join(home, ".parallel-intellect", "pintellect.db")); err != nil {
		t.Fatalf("default database was not created: %v", err)
	}
}
