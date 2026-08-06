package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type GitFingerprinter struct {
	Binary string
}

func (g GitFingerprinter) Fingerprint(ctx context.Context, worktreePath string) (Workspace, error) {
	head, err := g.output(ctx, worktreePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve validation HEAD: %w", err)
	}
	status, err := g.bytes(ctx, worktreePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Workspace{}, fmt.Errorf("inspect validation workspace: %w", err)
	}
	diff, err := g.bytes(ctx, worktreePath, "diff", "--binary", "--no-ext-diff", "HEAD", "--")
	if err != nil {
		return Workspace{}, fmt.Errorf("fingerprint tracked changes: %w", err)
	}
	untracked, err := g.bytes(ctx, worktreePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Workspace{}, fmt.Errorf("list untracked files: %w", err)
	}

	hash := sha256.New()
	writeHashPart(hash, status)
	writeHashPart(hash, diff)
	for _, name := range splitNUL(untracked) {
		writeHashPart(hash, []byte(name))
		path := filepath.Join(worktreePath, filepath.FromSlash(name))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return Workspace{}, fmt.Errorf("inspect untracked file %q: %w", name, statErr)
		}
		writeHashPart(hash, []byte(info.Mode().String()))
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return Workspace{}, fmt.Errorf("read untracked symlink %q: %w", name, readErr)
			}
			content = []byte(target)
		} else if info.Mode().IsRegular() {
			content, statErr = os.ReadFile(path)
			if statErr != nil {
				return Workspace{}, fmt.Errorf("read untracked file %q: %w", name, statErr)
			}
		}
		writeHashPart(hash, content)
	}
	return Workspace{HeadSHA: strings.ToLower(head), DirtyTreeHash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (g GitFingerprinter) output(ctx context.Context, path string, args ...string) (string, error) {
	value, err := g.bytes(ctx, path, args...)
	return strings.TrimSpace(string(value)), err
}

func (g GitFingerprinter) bytes(ctx context.Context, path string, args ...string) ([]byte, error) {
	binary := g.Binary
	if binary == "" {
		binary = "git"
	}
	commandArgs := append([]string{"-C", path}, args...)
	output, err := exec.CommandContext(ctx, binary, commandArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

type ProcessEnvironment struct {
	Values []string
}

func (e ProcessEnvironment) Fingerprint() (string, error) {
	values := e.Values
	if values == nil {
		values = os.Environ()
	}
	filtered := make([]string, 0, len(values)+2)
	for _, value := range values {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "PWD", "OLDPWD", "SHLVL", "_":
			continue
		}
		filtered = append(filtered, value)
	}
	filtered = append(filtered, "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	sort.Strings(filtered)
	return hashJSON(filtered)
}

func writeHashPart(hash interface{ Write([]byte) (int, error) }, value []byte) {
	length := fmt.Sprintf("%d:", len(value))
	_, _ = hash.Write([]byte(length))
	_, _ = hash.Write(value)
}

func splitNUL(value []byte) []string {
	parts := strings.Split(string(value), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
