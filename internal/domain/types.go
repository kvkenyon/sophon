// Package domain contains the durable value types shared across Sophon
// packages.
package domain

type TaskID string

type TaskKind string

const (
	TaskImplementation TaskKind = "implementation"
)

type DeliveryMode string

const (
	DeliveryPR     DeliveryMode = "pr"
	DeliveryBranch DeliveryMode = "branch"
)

type VerificationResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
}

// WorkerResult is the strict version 1 worker completion schema published as
// an attempt's result.json.
type WorkerResult struct {
	Version      int                  `json:"version"`
	Status       string               `json:"status"`
	Summary      string               `json:"summary"`
	Verification []VerificationResult `json:"verification"`
	ChangedFiles []string             `json:"changed_files"`
	Risks        []string             `json:"risks"`
}
