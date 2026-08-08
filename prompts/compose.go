package prompts

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Compose renders the commander runtime prompt: every markdown file in the
// commander prompt set, concatenated in sorted order with source headers.
// When skillDir is non-empty, materialized-skill load triggers for the
// commander skill set are appended. It replaces the deleted commander prompt
// composer; the caller materializes the per-session skill directory.
func Compose(skillDir string) (string, error) {
	promptFS, root, err := Set("commander")
	if err != nil {
		return "", err
	}
	var paths []string
	if err := fs.WalkDir(promptFS, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
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
		content, err := fs.ReadFile(promptFS, path)
		if err != nil {
			return "", fmt.Errorf("read commander prompt %s: %w", path, err)
		}
		fmt.Fprintf(&body, "\n\n<!-- commander prompt: %s -->\n%s", filepath.ToSlash(path), content)
	}
	if strings.TrimSpace(skillDir) != "" {
		body.WriteString(SkillTriggers(skillDir, CommanderSkills))
	}
	return strings.TrimSpace(body.String()) + "\n", nil
}
