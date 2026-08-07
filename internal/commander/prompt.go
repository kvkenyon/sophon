package commander

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"parallel-intellect/internal/db"
)

type PromptComposer struct {
	// Dir overrides prompt discovery. Absolute paths are used directly; relative
	// paths are resolved against the registered project and then InstallDir.
	Dir        string
	InstallDir string
}

func (c PromptComposer) Compose(snapshot db.CommanderLaunchContext) (string, error) {
	dir, err := c.resolveDir(snapshot.ProjectPath)
	if err != nil {
		return "", err
	}
	var paths []string
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("read commander prompt set: %w", err)
	}
	if len(paths) == 0 {
		return "", errors.New("commander prompt set contains no markdown files")
	}
	sort.Strings(paths)
	var body strings.Builder
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read commander prompt %s: %w", path, err)
		}
		fmt.Fprintf(&body, "\n\n<!-- commander prompt: %s -->\n%s", filepath.ToSlash(path), content)
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode commander mission snapshot: %w", err)
	}
	fmt.Fprintf(&body, `

# Bound mission

Mission ID: %s
Project: %s

## Objective

%s

## Acceptance criteria
`, snapshot.Mission.ID, snapshot.ProjectName, snapshot.Mission.Objective)
	if len(snapshot.Mission.AcceptanceCriteria) == 0 {
		body.WriteString("\n- No explicit acceptance criteria were recorded.\n")
	} else {
		for _, criterion := range snapshot.Mission.AcceptanceCriteria {
			fmt.Fprintf(&body, "\n- %s", criterion.Description)
		}
		body.WriteByte('\n')
	}
	fmt.Fprintf(&body, "\n## Current mission state snapshot\n\n```json\n%s\n```\n", encoded)
	return strings.TrimSpace(body.String()) + "\n", nil
}

func (c PromptComposer) resolveDir(projectPath string) (string, error) {
	relative := strings.TrimSpace(c.Dir)
	if relative == "" {
		relative = filepath.Join("prompts", "commander")
	}
	if filepath.IsAbs(relative) {
		return relative, nil
	}
	candidates := make([]string, 0, 2)
	if projectPath = strings.TrimSpace(projectPath); projectPath != "" {
		candidates = append(candidates, filepath.Join(projectPath, relative))
	}
	if installDir := strings.TrimSpace(c.InstallDir); installDir != "" {
		candidates = append(candidates, filepath.Join(installDir, relative))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect commander prompt set %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("commander prompt set not found relative to registered project or binary install directory (checked %s)", strings.Join(candidates, ", "))
}
