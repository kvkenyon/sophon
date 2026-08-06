package validation

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// CommandValidator runs an explicitly configured command without involving a
// worker runtime. Non-zero command exits are validation failures, not pipeline
// errors, so they can be retained as cacheable evidence.
type CommandValidator struct {
	ValidationKind Kind
	VersionString  string
	Arguments      []string
}

func (v CommandValidator) Kind() Kind        { return v.ValidationKind }
func (v CommandValidator) Version() string   { return v.VersionString }
func (v CommandValidator) Command() []string { return append([]string(nil), v.Arguments...) }

func (v CommandValidator) Run(ctx context.Context, worktreePath string) (Result, error) {
	started := time.Now().UTC()
	result := Result{StartedAt: started}
	if len(v.Arguments) == 0 || strings.TrimSpace(v.Arguments[0]) == "" {
		return result, errors.New("validator command is required")
	}
	command := exec.CommandContext(ctx, v.Arguments[0], v.Arguments[1:]...)
	command.Dir = worktreePath
	output, err := command.CombinedOutput()
	result.Duration = time.Since(started)
	result.Output = string(output)
	if err == nil {
		result.Status = Passed
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.Status = Failed
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

// ShellValidator is the CLI-facing validator. Its exact shell invocation is
// included in the command hash.
func ShellValidator(kind Kind, version, command string) CommandValidator {
	return CommandValidator{
		ValidationKind: kind,
		VersionString:  version,
		Arguments:      []string{"/bin/sh", "-c", command},
	}
}
