// Package treehouse owns the external worktree-lease reliability boundary.
package treehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type Allocation struct {
	WorktreePath string
	LeaseID      string
	LeaseHolder  string
	LeasedAt     time.Time
}

type WorktreeStatus struct {
	Name         string     `json:"name"`
	WorktreePath string     `json:"path"`
	Status       string     `json:"status"`
	LeaseID      string     `json:"lease_id"`
	LeaseHolder  string     `json:"lease_holder"`
	LeasedAt     *time.Time `json:"leased_at"`
}

type CLI interface {
	Acquire(context.Context, string, string) (Allocation, error)
	Release(context.Context, string, Allocation) error
	Status(context.Context, string) ([]WorktreeStatus, error)
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, []byte, error)
}

type execRunner struct{ binary string }

func (r execRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, r.binary, args...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type CommandClient struct {
	runner commandRunner
}

func NewCommandClient(binary string) *CommandClient {
	if binary == "" {
		binary = "treehouse"
	}
	return &CommandClient{runner: execRunner{binary: binary}}
}

func (c *CommandClient) Acquire(ctx context.Context, projectPath, holder string) (Allocation, error) {
	if strings.TrimSpace(projectPath) == "" || strings.TrimSpace(holder) == "" {
		return Allocation{}, errors.New("project path and lease holder are required")
	}
	stdout, stderr, err := c.run(ctx, projectPath, "get", "--lease", "--lease-holder", holder, "--json")
	if err != nil {
		return Allocation{}, commandError("treehouse get", err, stderr)
	}
	var response struct {
		Path        string    `json:"path"`
		LeaseID     string    `json:"lease_id"`
		LeaseHolder string    `json:"lease_holder"`
		LeasedAt    time.Time `json:"leased_at"`
	}
	if err := decodeOne(stdout, &response); err != nil {
		return Allocation{}, fmt.Errorf("decode treehouse get JSON: %w", err)
	}
	allocation := Allocation{WorktreePath: response.Path, LeaseID: response.LeaseID,
		LeaseHolder: response.LeaseHolder, LeasedAt: response.LeasedAt}
	if response.Path == "" || response.LeaseID == "" || response.LeaseHolder == "" {
		return allocation, errors.New("treehouse get returned an incomplete lease identity")
	}
	if response.LeaseHolder != holder {
		return allocation, fmt.Errorf("treehouse returned holder %q, want %q", response.LeaseHolder, holder)
	}
	return allocation, nil
}

func (c *CommandClient) Release(ctx context.Context, projectPath string, lease Allocation) error {
	if projectPath == "" || lease.WorktreePath == "" || lease.LeaseID == "" || lease.LeaseHolder == "" {
		return errors.New("project path and complete lease identity are required")
	}
	_, stderr, err := c.run(ctx, projectPath, "return", "--force",
		"--if-lease-id", lease.LeaseID,
		"--if-lease-holder", lease.LeaseHolder,
		lease.WorktreePath)
	if err != nil {
		return commandError("treehouse return", err, stderr)
	}
	return nil
}

func (c *CommandClient) Status(ctx context.Context, projectPath string) ([]WorktreeStatus, error) {
	if projectPath == "" {
		return nil, errors.New("project path is required")
	}
	stdout, stderr, err := c.run(ctx, projectPath, "status", "--json")
	if err != nil {
		return nil, commandError("treehouse status", err, stderr)
	}
	var statuses []WorktreeStatus
	if err := decodeOne(stdout, &statuses); err != nil {
		return nil, fmt.Errorf("decode treehouse status JSON: %w", err)
	}
	return statuses, nil
}

func (c *CommandClient) run(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	if c.runner == nil {
		return nil, nil, errors.New("Treehouse command runner is not configured")
	}
	return c.runner.Run(ctx, dir, args...)
}

func decodeOne(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func commandError(operation string, err error, stderr []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, detail)
}
