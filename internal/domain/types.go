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
	DeliveryLocal  DeliveryMode = "local"
	DeliveryPR     DeliveryMode = "pr"
	DeliveryBranch DeliveryMode = "branch"
)

// ReviewPosture is immutable task intake describing whether a local Read the
// Code review participates in delivery eligibility. The empty value is read
// as ReviewOff so tasks created before the field existed remain compatible.
type ReviewPosture string

const (
	ReviewOff      ReviewPosture = "off"
	ReviewOptional ReviewPosture = "optional"
	ReviewRequired ReviewPosture = "required"
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
