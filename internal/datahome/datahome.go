// Package datahome resolves Sophon's per-user data directory.
package datahome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OverrideEnv selects the Sophon data home directly, bypassing home-directory
// resolution. It exists so tests and embedded callers can run hermetically.
const OverrideEnv = "SOPHON_DATA_HOME"

const dirName = ".sophon"

// Dir returns the Sophon data home directory: the SOPHON_DATA_HOME override
// when set, otherwise ~/.sophon.
func Dir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(OverrideEnv)); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, dirName), nil
}

// AbsDir returns Dir resolved to a clean absolute path. Spawn propagates this
// exact value into worker runtime processes and the generated brief so the
// worker's own commands never depend on ambient environment or working
// directory.
func AbsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(dir)
}
