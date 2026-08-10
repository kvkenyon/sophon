package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"sophon/internal/datahome"
	"sophon/internal/workspace"
)

var (
	piLookPath   = exec.LookPath
	piExec       = syscall.Exec
	piExecutable = os.Executable
)

// piCommand starts an ordinary Pi process and then disappears. It deliberately
// does not attach a commander, start the notification monitor, or create any
// Sophon records: Pi remains a disposable operator interface.
func piCommand(_ context.Context, args []string) error {
	launcherArgs, passthrough, err := splitPiArgs(args)
	if err != nil {
		return &exitError{2, err}
	}
	flags := flag.NewFlagSet("pi", flag.ContinueOnError)
	workspaceRoot := flags.String("workspace", "", "Sophon workspace root")
	piBinary := flags.String("pi", "pi", "Pi executable")
	extensionPath := flags.String("extension", "", "Pi presentation extension path")
	if err := flags.Parse(launcherArgs); err != nil {
		return &exitError{2, err}
	}
	if len(flags.Args()) != 0 {
		return &exitError{2, errors.New("pi accepts Pi options only after --")}
	}
	if strings.TrimSpace(*workspaceRoot) == "" {
		return &exitError{2, errors.New("pi requires --workspace ROOT")}
	}
	marker, err := workspace.Read(*workspaceRoot)
	if err != nil {
		return fmt.Errorf("validate Pi workspace: %w", err)
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return fmt.Errorf("resolve Pi data home: %w", err)
	}
	prompt, err := commanderPrompt(home)
	if err != nil {
		return err
	}
	extension, err := resolvePiExtension(*extensionPath)
	if err != nil {
		return err
	}
	binary, err := piLookPath(strings.TrimSpace(*piBinary))
	if err != nil {
		return fmt.Errorf("Pi executable %q not found; install Pi or pass --pi PATH: %w", *piBinary, err)
	}
	argv := append([]string{binary, "--system-prompt", prompt, "--extension", extension}, passthrough...)
	env := replacePiEnv(os.Environ(),
		"SOPHON_PI=1",
		"SOPHON_WORKSPACE_ROOT="+marker.Root,
		"SOPHON_WORKSPACE_ID="+marker.ID,
		datahome.OverrideEnv+"="+home,
	)
	if err := os.Chdir(marker.Root); err != nil {
		return fmt.Errorf("enter Pi workspace: %w", err)
	}
	if err := piExec(binary, argv, env); err != nil {
		return fmt.Errorf("replace Sophon with Pi: %w", err)
	}
	return nil
}

func splitPiArgs(args []string) ([]string, []string, error) {
	for index, arg := range args {
		if arg == "--" {
			for _, piArg := range args[index+1:] {
				if strings.IndexByte(piArg, 0) >= 0 {
					return nil, nil, errors.New("Pi passthrough contains an invalid NUL byte")
				}
			}
			return args[:index], args[index+1:], nil
		}
	}
	return args, nil, nil
}

func replacePiEnv(base []string, values ...string) []string {
	keys := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		keys[key] = struct{}{}
	}
	env := make([]string, 0, len(base)+len(values))
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if _, replace := keys[key]; !replace {
			env = append(env, value)
		}
	}
	return append(env, values...)
}

// resolvePiExtension is the only launcher-to-presentation boundary. The
// presentation package owns the default source path; callers can override it
// explicitly for development and tests without installing anything globally.
func resolvePiExtension(explicit string) (string, error) {
	candidate := strings.TrimSpace(explicit)
	if candidate == "" {
		executable, err := piExecutable()
		if err != nil {
			return "", fmt.Errorf("resolve Pi presentation extension: locate Sophon executable: %w", err)
		}
		candidate = filepath.Join(filepath.Dir(executable), "integrations", "pi", "index.ts")
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve Pi presentation extension: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("resolve Pi presentation extension %q: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return "", fmt.Errorf("resolve Pi presentation extension %q: must be a regular file or directory", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve Pi presentation extension %q: %w", abs, err)
	}
	return resolved, nil
}
