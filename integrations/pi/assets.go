// Package pi contains the production assets for Sophon's Pi presentation.
package pi

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed index.ts src/calm.ts src/presentation.ts src/rendering.ts
var presentationAssets embed.FS

var assetNames = []string{
	"index.ts",
	"src/calm.ts",
	"src/presentation.ts",
	"src/rendering.ts",
}

// Materialize writes the exact embedded presentation into a private,
// content-addressed directory below dataHome and returns its entry point. A
// previously materialized version is reused only after exact verification.
func Materialize(dataHome string) (string, error) {
	root, err := absoluteClean(dataHome)
	if err != nil {
		return "", fmt.Errorf("resolve Pi presentation data home: %w", err)
	}
	assets, identity, err := embeddedAssets()
	if err != nil {
		return "", err
	}
	piRoot := filepath.Join(root, "pi")
	parent := filepath.Join(piRoot, "extensions")
	for _, directory := range []string{root, piRoot, parent} {
		if err := privateDirectories(directory); err != nil {
			return "", fmt.Errorf("prepare Pi presentation directory: %w", err)
		}
	}
	target := filepath.Join(parent, identity)
	if info, err := os.Lstat(target); err == nil {
		if err := verifyMaterialized(target, assets); err != nil {
			return "", fmt.Errorf("verify existing Pi presentation: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("existing Pi presentation path is not a directory")
		}
		return filepath.Join(target, "index.ts"), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect Pi presentation target: %w", err)
	}

	stage, err := os.MkdirTemp(parent, ".materializing-")
	if err != nil {
		return "", fmt.Errorf("stage Pi presentation: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return "", fmt.Errorf("protect Pi presentation stage: %w", err)
	}
	for name, content := range assets {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("prepare Pi presentation asset directory: %w", err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("protect Pi presentation asset directory: %w", err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return "", fmt.Errorf("write Pi presentation asset %q: %w", name, err)
		}
	}
	if err := verifyMaterialized(stage, assets); err != nil {
		return "", fmt.Errorf("verify staged Pi presentation: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		if errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrPermission) {
			if verifyErr := verifyMaterialized(target, assets); verifyErr == nil {
				return filepath.Join(target, "index.ts"), nil
			}
		}
		return "", fmt.Errorf("publish Pi presentation: %w", err)
	}
	return filepath.Join(target, "index.ts"), nil
}

func embeddedAssets() (map[string][]byte, string, error) {
	assets := make(map[string][]byte, len(assetNames))
	hash := sha256.New()
	for _, name := range assetNames {
		content, err := presentationAssets.ReadFile(name)
		if err != nil {
			return nil, "", fmt.Errorf("read embedded Pi presentation asset %q: %w", name, err)
		}
		assets[name] = content
		fmt.Fprintf(hash, "%s\x00%d\x00", name, len(content))
		_, _ = hash.Write(content)
	}
	return assets, hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyMaterialized(root string, assets map[string][]byte) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("directory permissions are not private")
	}
	seen := make(map[string]bool, len(assets))
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symlink", rel)
		}
		if entry.IsDir() {
			if info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("directory %q permissions are not private", rel)
			}
			return nil
		}
		want, ok := assets[rel]
		if !ok || !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected asset %q", rel)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("asset %q permissions are not private", rel)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("asset %q does not match embedded content", rel)
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	for name := range assets {
		if !seen[name] {
			return fmt.Errorf("asset %q is missing", name)
		}
	}
	return nil
}

func privateDirectories(path string) error {
	path, err := absoluteClean(path)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q must be a real directory", path)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	path = filepath.Join(resolvedParent, filepath.Base(path))
	volume := filepath.VolumeName(path)
	parts := strings.Split(strings.TrimPrefix(path, volume+string(filepath.Separator)), string(filepath.Separator))
	current := volume + string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%q must be a real directory", current)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("protect %q: %w", path, err)
		}
	}
	return nil
}

func absoluteClean(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	// Keep a requested existing path literal so privateDirectories can refuse a
	// symlink at the data-home boundary. For a not-yet-created path, normalize
	// only its existing ancestors (macOS commonly exposes /tmp through one).
	if _, err := os.Lstat(abs); err == nil {
		return abs, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var tail []string
	probe := abs
	for {
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("find existing parent for %q", abs)
		}
		tail = append([]string{filepath.Base(probe)}, tail...)
		probe = parent
		if _, err := os.Lstat(probe); err == nil {
			resolved, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
}

// AssetNames returns the production files included in Sophon's binary.
func AssetNames() []string {
	names := append([]string(nil), assetNames...)
	sort.Strings(names)
	return names
}
