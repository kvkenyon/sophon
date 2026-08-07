package datahome

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesNewDirectoryByDefault(t *testing.T) {
	home := t.TempDir()
	location, err := resolve(home, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if location.Dir != filepath.Join(home, ".sophon") || location.Legacy {
		t.Fatalf("location = %+v", location)
	}
}

func TestResolveFallsBackToLegacyDirectory(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".parallel-intellect")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	var notice bytes.Buffer
	location, err := resolve(home, &notice)
	if err != nil {
		t.Fatal(err)
	}
	if location.Dir != legacy || !location.Legacy {
		t.Fatalf("location = %+v", location)
	}
	if location.DatabasePath() != filepath.Join(legacy, "pintellect.db") ||
		location.DaemonPIDPath() != filepath.Join(legacy, "pintellectd.pid") {
		t.Fatalf("legacy paths were not preserved: db=%q pid=%q", location.DatabasePath(), location.DaemonPIDPath())
	}
	if got := notice.String(); !strings.Contains(got, "mv ~/.parallel-intellect ~/.sophon") || strings.Count(got, "\n") != 1 {
		t.Fatalf("notice = %q", got)
	}
}

func TestResolvePrefersNewDirectory(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".sophon", ".parallel-intellect"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var notice bytes.Buffer
	location, err := resolve(home, &notice)
	if err != nil {
		t.Fatal(err)
	}
	if location.Dir != filepath.Join(home, ".sophon") || location.Legacy || notice.Len() != 0 {
		t.Fatalf("location=%+v notice=%q", location, notice.String())
	}
}

func TestMovedDirectoryKeepsUsingLegacyFileNames(t *testing.T) {
	home := t.TempDir()
	current := filepath.Join(home, ".sophon")
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pintellect.db", "pintellectd.pid", "pintellectd.log", "pintellectd.health.json"} {
		if err := os.WriteFile(filepath.Join(current, name), []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	location, err := resolve(home, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if location.Legacy {
		t.Fatal("moved .sophon directory must be the current location")
	}
	for got, want := range map[string]string{
		location.DatabasePath():     "pintellect.db",
		location.DaemonPIDPath():    "pintellectd.pid",
		location.DaemonLogPath():    "pintellectd.log",
		location.DaemonHealthPath(): "pintellectd.health.json",
	} {
		if filepath.Base(got) != want {
			t.Errorf("path = %q, want filename %q", got, want)
		}
	}
}
