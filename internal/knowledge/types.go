// Package knowledge defines governed durable knowledge and its deterministic
// self-improvement boundary.
package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"sophon/internal/domain"
)

type ID string
type Scope string
type Status string
type Origin string

const (
	ScopeImmutablePolicy Scope = "immutable-policy"
	ScopeProject         Scope = "project"
	ScopeLearned         Scope = "learned"
	ScopeMission         Scope = "mission"

	StatusCandidate  Status = "candidate"
	StatusActive     Status = "active"
	StatusRejected   Status = "rejected"
	StatusSuperseded Status = "superseded"

	OriginOperator  Origin = "operator"
	OriginCommander Origin = "commander"
	OriginAgent     Origin = "agent"
	OriginSystem    Origin = "system"
)

var (
	ErrCriticalPolicyWrite = errors.New("agent-originated critical-policy write refused")
	ErrPromotionAuthority  = errors.New("knowledge promotion requires operator or commander authority")
)

var criticalKinds = map[string]struct{}{
	"task-state-machine": {}, "worktree-lease-policy": {}, "destructive-operation-policy": {},
	"credentials-policy": {}, "merge-policy": {}, "pr-delivery-policy": {},
	"security-boundary": {}, "completion-requirements": {}, "operator-authority": {},
	"delivery-policy": {},
	"state-machine":   {}, "lease-policy": {}, "destructive-policy": {}, "credentials": {},
	"merge-authority": {}, "security-rules": {}, "completion-requirement": {},
}

type Entry struct {
	ID                 ID                 `json:"id"`
	ProjectID          domain.ProjectID   `json:"project_id"`
	MissionID          *domain.MissionID  `json:"mission_id,omitempty"`
	Scope              Scope              `json:"scope"`
	Kind               string             `json:"kind"`
	Content            string             `json:"content"`
	CreatedBy          string             `json:"created_by"`
	Origin             Origin             `json:"origin"`
	TriggerTaskID      *domain.TaskID     `json:"trigger_task_id,omitempty"`
	EvidenceArtifactID *domain.ArtifactID `json:"evidence_artifact_id,omitempty"`
	Confidence         float64            `json:"confidence"`
	Status             Status             `json:"status"`
	CreatedAt          time.Time          `json:"created_at"`
	SupersededBy       *ID                `json:"superseded_by,omitempty"`
}

func ValidateProposal(entry Entry) error {
	if entry.ProjectID == "" || strings.TrimSpace(entry.Kind) == "" || strings.TrimSpace(entry.Content) == "" || strings.TrimSpace(entry.CreatedBy) == "" {
		return errors.New("project, kind, content, and creator are required")
	}
	if entry.Confidence < 0 || entry.Confidence > 1 {
		return errors.New("knowledge confidence must be between zero and one")
	}
	switch entry.Scope {
	case ScopeImmutablePolicy, ScopeProject, ScopeLearned, ScopeMission:
	default:
		return fmt.Errorf("unknown knowledge scope %q", entry.Scope)
	}
	if entry.Scope == ScopeMission && (entry.MissionID == nil || *entry.MissionID == "") {
		return errors.New("mission-scoped knowledge requires a mission")
	}
	if entry.Origin != OriginOperator && entry.Origin != OriginCommander && entry.Origin != OriginAgent && entry.Origin != OriginSystem {
		return fmt.Errorf("unknown knowledge origin %q", entry.Origin)
	}
	return AuthorizeWrite(entry.Origin, entry.Scope, entry.Kind)
}

// AuthorizeWrite is the mechanical invariant-14 boundary shared by create and
// lifecycle operations. Learned behavior cannot weaken these surfaces.
func AuthorizeWrite(origin Origin, scope Scope, kind string) error {
	normalized := strings.NewReplacer("_", "-", " ", "-").Replace(strings.ToLower(strings.TrimSpace(kind)))
	_, critical := criticalKinds[normalized]
	if origin == OriginAgent && (scope == ScopeImmutablePolicy || critical) {
		return ErrCriticalPolicyWrite
	}
	return nil
}

func AuthorizePromotion(origin Origin, entry Entry) error {
	if err := AuthorizeWrite(origin, entry.Scope, entry.Kind); err != nil {
		return err
	}
	if origin != OriginOperator && origin != OriginCommander {
		return ErrPromotionAuthority
	}
	if entry.Status != StatusCandidate {
		return fmt.Errorf("only candidate knowledge may be promoted, got %s", entry.Status)
	}
	return nil
}
