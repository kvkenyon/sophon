package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sophon/internal/domain"
	"sophon/internal/id"
	"sophon/internal/knowledge"
)

type ProposeKnowledgeInput struct {
	ProjectID          domain.ProjectID   `json:"project_id"`
	MissionID          *domain.MissionID  `json:"mission_id,omitempty"`
	Scope              knowledge.Scope    `json:"scope"`
	Kind               string             `json:"kind"`
	Content            string             `json:"content"`
	CreatedBy          string             `json:"created_by"`
	Origin             knowledge.Origin   `json:"origin"`
	TriggerTaskID      *domain.TaskID     `json:"trigger_task_id,omitempty"`
	EvidenceArtifactID *domain.ArtifactID `json:"evidence_artifact_id,omitempty"`
	Confidence         float64            `json:"confidence"`
}

func (s *Store) ProposeKnowledge(ctx context.Context, commandID domain.CommandID, in ProposeKnowledgeInput) (knowledge.Entry, error) {
	candidate := knowledge.Entry{ProjectID: in.ProjectID, MissionID: in.MissionID, Scope: in.Scope,
		Kind: in.Kind, Content: in.Content, CreatedBy: in.CreatedBy, Origin: in.Origin,
		TriggerTaskID: in.TriggerTaskID, EvidenceArtifactID: in.EvidenceArtifactID,
		Confidence: in.Confidence, Status: knowledge.StatusCandidate}
	if err := knowledge.ValidateProposal(candidate); err != nil {
		return knowledge.Entry{}, err
	}
	return runCommand(ctx, s, commandID, "knowledge.propose", in, func(tx *sql.Tx) (knowledge.Entry, error) {
		if err := verifyKnowledgeProvenance(ctx, tx, candidate); err != nil {
			return knowledge.Entry{}, err
		}
		rawID, err := id.New("knw")
		if err != nil {
			return knowledge.Entry{}, err
		}
		now := time.Now().UTC()
		candidate.ID, candidate.CreatedAt = knowledge.ID(rawID), now
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge(
			id, project_id, mission_id, scope, kind, content, created_by, origin,
			trigger_task_id, evidence_artifact_id, confidence, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, candidate.ID, candidate.ProjectID,
			missionIDValue(candidate.MissionID), candidate.Scope, candidate.Kind, candidate.Content,
			candidate.CreatedBy, candidate.Origin, taskIDValue(candidate.TriggerTaskID),
			artifactIDValue(candidate.EvidenceArtifactID), candidate.Confidence, candidate.Status,
			formatTime(now)); err != nil {
			return knowledge.Entry{}, fmt.Errorf("insert knowledge candidate: %w", err)
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: candidate.MissionID, TaskID: candidate.TriggerTaskID,
			Actor: candidate.CreatedBy, Type: "knowledge.proposed", CommandID: &commandID,
			Payload: map[string]any{"knowledge_id": candidate.ID, "scope": candidate.Scope,
				"kind": candidate.Kind, "status": candidate.Status, "confidence": candidate.Confidence}}); err != nil {
			return knowledge.Entry{}, err
		}
		return candidate, nil
	})
}

type TransitionKnowledgeInput struct {
	KnowledgeID  knowledge.ID     `json:"knowledge_id"`
	To           knowledge.Status `json:"to"`
	Actor        string           `json:"actor"`
	Origin       knowledge.Origin `json:"origin"`
	SupersededBy *knowledge.ID    `json:"superseded_by,omitempty"`
}

func (s *Store) TransitionKnowledge(ctx context.Context, commandID domain.CommandID, in TransitionKnowledgeInput) (knowledge.Entry, error) {
	if in.KnowledgeID == "" || strings.TrimSpace(in.Actor) == "" {
		return knowledge.Entry{}, errors.New("knowledge id and actor are required")
	}
	return runCommand(ctx, s, commandID, "knowledge.transition", in, func(tx *sql.Tx) (knowledge.Entry, error) {
		current, err := getKnowledgeTx(ctx, tx, in.KnowledgeID)
		if err != nil {
			return knowledge.Entry{}, err
		}
		if in.To == knowledge.StatusActive {
			if err := knowledge.AuthorizePromotion(in.Origin, current); err != nil {
				return knowledge.Entry{}, err
			}
		} else {
			if err := knowledge.AuthorizeWrite(in.Origin, current.Scope, current.Kind); err != nil {
				return knowledge.Entry{}, err
			}
			if in.Origin != knowledge.OriginOperator && in.Origin != knowledge.OriginCommander {
				return knowledge.Entry{}, knowledge.ErrPromotionAuthority
			}
		}
		if current.Status != knowledge.StatusCandidate && !(current.Status == knowledge.StatusActive && in.To == knowledge.StatusSuperseded) {
			return knowledge.Entry{}, fmt.Errorf("knowledge in %s cannot transition to %s", current.Status, in.To)
		}
		switch in.To {
		case knowledge.StatusActive, knowledge.StatusRejected:
			if in.SupersededBy != nil {
				return knowledge.Entry{}, errors.New("only superseded knowledge names a replacement")
			}
		case knowledge.StatusSuperseded:
			if in.SupersededBy == nil || *in.SupersededBy == "" || *in.SupersededBy == current.ID {
				return knowledge.Entry{}, errors.New("superseded knowledge requires a distinct replacement")
			}
			replacement, err := getKnowledgeTx(ctx, tx, *in.SupersededBy)
			if err != nil {
				return knowledge.Entry{}, err
			}
			if replacement.ProjectID != current.ProjectID || replacement.Status != knowledge.StatusActive {
				return knowledge.Entry{}, errors.New("replacement must be active knowledge in the same project")
			}
		default:
			return knowledge.Entry{}, fmt.Errorf("unsupported knowledge transition %s", in.To)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge SET status = ?, superseded_by = ? WHERE id = ? AND status = ?`,
			in.To, knowledgeIDValue(in.SupersededBy), current.ID, current.Status); err != nil {
			return knowledge.Entry{}, fmt.Errorf("transition knowledge: %w", err)
		}
		updated, err := getKnowledgeTx(ctx, tx, current.ID)
		if err != nil {
			return knowledge.Entry{}, err
		}
		if err := appendEvent(ctx, tx, eventInput{MissionID: updated.MissionID, TaskID: updated.TriggerTaskID,
			Actor: in.Actor, Type: "knowledge." + string(in.To), CommandID: &commandID,
			Payload: map[string]any{"knowledge_id": updated.ID, "from": current.Status,
				"to": updated.Status, "superseded_by": updated.SupersededBy}}); err != nil {
			return knowledge.Entry{}, err
		}
		return updated, nil
	})
}

func (s *Store) Knowledge(ctx context.Context, knowledgeID knowledge.ID) (knowledge.Entry, error) {
	entry, err := scanKnowledge(s.db.QueryRowContext(ctx, knowledgeSelect+" WHERE id = ?", knowledgeID))
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.Entry{}, ErrNotFound
	}
	return entry, err
}

type ListKnowledgeFilter struct {
	ProjectID domain.ProjectID
	MissionID domain.MissionID
	Scope     knowledge.Scope
	Status    knowledge.Status
}

func (s *Store) KnowledgeEntries(ctx context.Context, filter ListKnowledgeFilter) ([]knowledge.Entry, error) {
	query := knowledgeSelect + " WHERE 1=1"
	args := make([]any, 0, 4)
	if filter.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, filter.ProjectID)
	}
	if filter.MissionID != "" {
		query += " AND mission_id = ?"
		args = append(args, filter.MissionID)
	}
	if filter.Scope != "" {
		query += " AND scope = ?"
		args = append(args, filter.Scope)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	query += " ORDER BY created_at, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]knowledge.Entry, 0)
	for rows.Next() {
		entry, err := scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

const knowledgeSelect = `SELECT id, project_id, mission_id, scope, kind, content, created_by, origin,
	trigger_task_id, evidence_artifact_id, confidence, status, created_at, superseded_by FROM knowledge`

func getKnowledgeTx(ctx context.Context, tx *sql.Tx, knowledgeID knowledge.ID) (knowledge.Entry, error) {
	entry, err := scanKnowledge(tx.QueryRowContext(ctx, knowledgeSelect+" WHERE id = ?", knowledgeID))
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.Entry{}, ErrNotFound
	}
	return entry, err
}

func scanKnowledge(row rowScanner) (knowledge.Entry, error) {
	var entry knowledge.Entry
	var mission, triggerTask, evidence, created, superseded sql.NullString
	if err := row.Scan(&entry.ID, &entry.ProjectID, &mission, &entry.Scope, &entry.Kind, &entry.Content,
		&entry.CreatedBy, &entry.Origin, &triggerTask, &evidence, &entry.Confidence, &entry.Status,
		&created, &superseded); err != nil {
		return knowledge.Entry{}, err
	}
	if mission.Valid {
		value := domain.MissionID(mission.String)
		entry.MissionID = &value
	}
	if triggerTask.Valid {
		value := domain.TaskID(triggerTask.String)
		entry.TriggerTaskID = &value
	}
	if evidence.Valid {
		value := domain.ArtifactID(evidence.String)
		entry.EvidenceArtifactID = &value
	}
	if superseded.Valid {
		value := knowledge.ID(superseded.String)
		entry.SupersededBy = &value
	}
	parsed, err := parseTime(created.String)
	if err != nil {
		return knowledge.Entry{}, err
	}
	entry.CreatedAt = parsed
	return entry, nil
}

func verifyKnowledgeProvenance(ctx context.Context, tx *sql.Tx, entry knowledge.Entry) error {
	if entry.MissionID != nil {
		var project domain.ProjectID
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM missions WHERE id = ?", *entry.MissionID).Scan(&project); err != nil {
			return mapNotFound("load knowledge mission", err)
		}
		if project != entry.ProjectID {
			return errors.New("knowledge mission belongs to another project")
		}
	}
	if entry.TriggerTaskID != nil {
		var mission domain.MissionID
		var project domain.ProjectID
		if err := tx.QueryRowContext(ctx, `SELECT t.mission_id, m.project_id FROM tasks t
			JOIN missions m ON m.id = t.mission_id WHERE t.id = ?`, *entry.TriggerTaskID).Scan(&mission, &project); err != nil {
			return mapNotFound("load knowledge trigger task", err)
		}
		if project != entry.ProjectID {
			return errors.New("knowledge trigger task belongs to another project")
		}
		if entry.MissionID != nil && mission != *entry.MissionID {
			return errors.New("knowledge trigger task belongs to another mission")
		}
	}
	if entry.EvidenceArtifactID != nil {
		var project domain.ProjectID
		if err := tx.QueryRowContext(ctx, `SELECT m.project_id FROM artifacts a JOIN tasks t ON t.id = a.task_id
			JOIN missions m ON m.id = t.mission_id WHERE a.id = ?`, *entry.EvidenceArtifactID).Scan(&project); err != nil {
			return mapNotFound("load knowledge evidence artifact", err)
		}
		if project != entry.ProjectID {
			return errors.New("knowledge evidence belongs to another project")
		}
	}
	return nil
}

func artifactIDValue(value *domain.ArtifactID) any {
	if value == nil {
		return nil
	}
	return *value
}
func knowledgeIDValue(value *knowledge.ID) any {
	if value == nil {
		return nil
	}
	return *value
}
