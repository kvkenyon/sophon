package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sophon/internal/workspace"
)

func TestPiCommandReplacesProcessWithCanonicalCommander(t *testing.T) {
	root := t.TempDir()
	marker, err := workspace.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	extension := filepath.Join(t.TempDir(), "presentation.ts")
	if err := os.WriteFile(extension, []byte("export default () => {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "fake-pi")
	if err := os.WriteFile(binary, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	originalLookPath, originalExec := piLookPath, piExec
	t.Cleanup(func() { piLookPath, piExec = originalLookPath, originalExec })
	piLookPath = func(name string) (string, error) {
		if name != "fake-pi" {
			t.Fatalf("lookup = %q", name)
		}
		return binary, nil
	}
	var gotPath string
	var gotArgv, gotEnv []string
	piExec = func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, append([]string(nil), argv...), append([]string(nil), env...)
		return nil
	}
	if err := piCommand(context.Background(), []string{"--workspace", root, "--pi", "fake-pi", "--extension", extension, "--", "--model", "openai/gpt-5", "--thinking", "high"}); err != nil {
		t.Fatal(err)
	}
	if cwd, _ := os.Getwd(); cwd != marker.Root {
		t.Fatalf("cwd = %q, want %q", cwd, marker.Root)
	}
	if gotPath != binary || !reflect.DeepEqual(gotArgv[len(gotArgv)-4:], []string{"--model", "openai/gpt-5", "--thinking", "high"}) {
		t.Fatalf("exec path=%q argv=%q", gotPath, gotArgv)
	}
	canonicalExtension, err := filepath.EvalSymlinks(extension)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) != 9 || gotArgv[0] != binary || gotArgv[1] != "--system-prompt" || gotArgv[3] != "--extension" || gotArgv[4] != canonicalExtension {
		t.Fatalf("launcher argv = %q", gotArgv)
	}
	if !strings.Contains(gotArgv[2], "# Sophon commander") {
		t.Fatalf("system prompt was not commander prompt: %q", gotArgv[2])
	}
	for _, want := range []string{"SOPHON_PI=1", "SOPHON_WORKSPACE_ROOT=" + marker.Root, "SOPHON_WORKSPACE_ID=" + marker.ID, "SOPHON_DATA_HOME=" + home} {
		if !containsEnv(gotEnv, want) {
			t.Fatalf("environment omits %q: %q", want, gotEnv)
		}
	}
}

func TestPiCommandUsesBundledPresentation(t *testing.T) {
	root := initializedWorkspace(t)
	home := t.TempDir()
	t.Setenv("SOPHON_DATA_HOME", home)
	originalLookPath, originalExec := piLookPath, piExec
	t.Cleanup(func() { piLookPath, piExec = originalLookPath, originalExec })
	piLookPath = func(string) (string, error) { return "/fake/pi", nil }
	var argv []string
	piExec = func(_ string, got []string, _ []string) error { argv = append([]string(nil), got...); return nil }
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	if err := piCommand(context.Background(), []string{"--workspace", root}); err != nil {
		t.Fatal(err)
	}
	if len(argv) < 5 || argv[3] != "--extension" {
		t.Fatalf("launcher argv = %q", argv)
	}
	if !strings.HasPrefix(argv[4], filepath.Join(home, "pi", "extensions")+string(filepath.Separator)) {
		t.Fatalf("bundled extension = %q", argv[4])
	}
	if _, err := os.Stat(argv[4]); err != nil {
		t.Fatalf("materialized extension: %v", err)
	}
}

func TestPiCommandRefusesBeforeProcessReplacement(t *testing.T) {
	originalExec := piExec
	t.Cleanup(func() { piExec = originalExec })
	piExec = func(string, []string, []string) error { t.Fatal("Pi was executed"); return nil }
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"missing workspace", nil, "requires --workspace"},
		{"unsafe workspace", []string{"--workspace", t.TempDir()}, "validate Pi workspace"},
		{"positional option", []string{"--workspace", t.TempDir(), "--model", "x"}, "flag provided but not defined"},
		{"missing extension", []string{"--workspace", initializedWorkspace(t), "--extension", filepath.Join(t.TempDir(), "missing.ts")}, "presentation extension"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := piCommand(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPiCommandPropagatesExecFailure(t *testing.T) {
	root := initializedWorkspace(t)
	extension := filepath.Join(t.TempDir(), "presentation.ts")
	if err := os.WriteFile(extension, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// piCommand changes directory immediately before syscall.Exec. The test
	// replacement returns to this process, so restore the process-wide fixture
	// before TempDir removes the workspace beneath subsequent tests.
	t.Cleanup(func() {
		if err := os.Chdir(oldCWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	originalLookPath, originalExec := piLookPath, piExec
	t.Cleanup(func() { piLookPath, piExec = originalLookPath, originalExec })
	piLookPath = func(string) (string, error) { return "/fake/pi", nil }
	piExec = func(string, []string, []string) error { return errors.New("exit status 23") }
	err = piCommand(context.Background(), []string{"--workspace", root, "--extension", extension})
	if err == nil || !strings.Contains(err.Error(), "exit status 23") {
		t.Fatalf("error = %v", err)
	}
}

func TestPiCommandRefusesMissingPi(t *testing.T) {
	root := initializedWorkspace(t)
	extension := filepath.Join(t.TempDir(), "presentation.ts")
	if err := os.WriteFile(extension, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	originalLookPath := piLookPath
	t.Cleanup(func() { piLookPath = originalLookPath })
	piLookPath = func(string) (string, error) { return "", errors.New("not found") }
	err := piCommand(context.Background(), []string{"--workspace", root, "--extension", extension})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestPiCommandPreservesPiExitAndSignal(t *testing.T) {
	for _, test := range []struct {
		name, program string
		signal        bool
	}{
		{"exit", "exit 23", false},
		{"signal", "kill -TERM $$", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initializedWorkspace(t)
			extension := filepath.Join(t.TempDir(), "presentation.ts")
			if err := os.WriteFile(extension, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			pi := filepath.Join(t.TempDir(), "fake-pi")
			if err := os.WriteFile(pi, []byte("#!/bin/sh\n"+test.program+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=TestPiProcessHelper", "--", "--workspace", root, "--pi", pi, "--extension", extension)
			command.Env = append(os.Environ(), "SOPHON_PI_HELPER=1", "SOPHON_DATA_HOME="+t.TempDir())
			err := command.Run()
			if err == nil {
				t.Fatal("Pi launcher unexpectedly succeeded")
			}
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run error = %T %v", err, err)
			}
			if test.signal {
				if exit.ProcessState.ExitCode() != -1 {
					t.Fatalf("signal exit code = %d", exit.ProcessState.ExitCode())
				}
			} else if exit.ProcessState.ExitCode() != 23 {
				t.Fatalf("exit code = %d", exit.ProcessState.ExitCode())
			}
		})
	}
}

func TestPiProcessHelper(t *testing.T) {
	if os.Getenv("SOPHON_PI_HELPER") != "1" {
		return
	}
	args := os.Args
	for index, arg := range args {
		if arg == "--" {
			if err := run(context.Background(), append([]string{"pi"}, args[index+1:]...)); err != nil {
				panic(err)
			}
			return
		}
	}
	panic("missing Pi helper delimiter")
}

func initializedWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := workspace.Init(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}
