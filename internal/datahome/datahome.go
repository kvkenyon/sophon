// Package datahome resolves Sophon's per-user state directory and legacy paths.
package datahome

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	currentDirName = ".sophon"
	// Legacy names are intentionally retained so existing installations continue
	// to use their database and daemon state until the operator moves the directory.
	legacyDirName      = ".parallel-intellect"
	legacyDatabaseName = "pintellect.db"
	legacyDaemonName   = "pintellectd"
)

var notices sync.Map

// Location is the selected Sophon state directory. Legacy is true only when
// ~/.sophon is absent and the former product directory exists.
type Location struct {
	Dir    string
	Legacy bool
}

func Resolve() (Location, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Location{}, fmt.Errorf("resolve home directory: %w", err)
	}
	location, err := resolve(home, os.Stderr)
	if err != nil {
		return Location{}, err
	}
	return location, nil
}

func resolve(home string, notice io.Writer) (Location, error) {
	current := filepath.Join(home, currentDirName)
	if _, err := os.Stat(current); err == nil {
		return Location{Dir: current}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Location{}, fmt.Errorf("inspect Sophon data directory: %w", err)
	}

	legacy := filepath.Join(home, legacyDirName)
	if _, err := os.Stat(legacy); err == nil {
		if _, alreadyPrinted := notices.LoadOrStore(legacy, struct{}{}); !alreadyPrinted {
			fmt.Fprintln(notice, "Sophon: using legacy data directory ~/.parallel-intellect; migrate with: mv ~/.parallel-intellect ~/.sophon")
		}
		return Location{Dir: legacy, Legacy: true}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Location{}, fmt.Errorf("inspect legacy Sophon data directory: %w", err)
	}
	return Location{Dir: current}, nil
}

func (l Location) DatabasePath() string {
	return l.statePath("sophon.db", legacyDatabaseName)
}

func (l Location) DaemonPIDPath() string    { return l.daemonPath(".pid") }
func (l Location) DaemonLogPath() string    { return l.daemonPath(".log") }
func (l Location) DaemonHealthPath() string { return l.daemonPath(".health.json") }

func (l Location) daemonPath(suffix string) string {
	return l.statePath("sophond"+suffix, legacyDaemonName+suffix)
}

func (l Location) statePath(currentName, legacyName string) string {
	current := filepath.Join(l.Dir, currentName)
	legacy := filepath.Join(l.Dir, legacyName)
	if l.Legacy {
		return legacy
	}
	// A documented `mv ~/.parallel-intellect ~/.sophon` intentionally leaves
	// legacy filenames inside the new directory. Prefer an existing new file,
	// then an existing legacy file, so that migration remains a single step.
	if _, err := os.Stat(current); err == nil {
		return current
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
}
