package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"sophon/internal/digest"
	"sophon/internal/domain"
	"sophon/internal/id"
	signalpolicy "sophon/internal/signals"
)

type RegenerateMissionDigestInput struct {
	MissionID domain.MissionID `json:"mission_id"`
	Actor     string           `json:"actor"`
	Reason    string           `json:"reason"`
}

func (s *Store) RegenerateMissionDigest(ctx context.Context, commandID domain.CommandID, in RegenerateMissionDigestInput) (digest.Artifact, error) {
	if in.MissionID == "" || in.Actor == "" {
		return digest.Artifact{}, errors.New("mission and actor are required")
	}
	return runCommand(ctx, s, commandID, "mission.digest.regenerate", in, func(tx *sql.Tx) (digest.Artifact, error) {
		return regenerateMissionDigestTx(ctx, tx, in.MissionID, in.Actor, in.Reason, &commandID)
	})
}

func (s *Store) LatestMissionDigest(ctx context.Context, missionID domain.MissionID) (digest.Artifact, error) {
	return scanMissionDigest(s.db.QueryRowContext(ctx, missionDigestSelect+` WHERE mission_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, missionID))
}

func regenerateMissionDigestTx(ctx context.Context, tx *sql.Tx, missionID domain.MissionID, actor, reason string, commandID *domain.CommandID) (digest.Artifact, error) {
	mission, err := scanMission(tx.QueryRowContext(ctx, missionSelect+" WHERE id = ?", missionID))
	if err != nil {
		return digest.Artifact{}, err
	}
	tasks, err := tasksTx(ctx, tx, missionID)
	if err != nil {
		return digest.Artifact{}, err
	}
	signals, err := signalsTx(ctx, tx, missionID)
	if err != nil {
		return digest.Artifact{}, err
	}
	events, err := eventsTx(ctx, tx, missionID)
	if err != nil {
		return digest.Artifact{}, err
	}
	content := digest.Render(digest.Input{Mission: mission, Tasks: tasks, Signals: signals, Events: events})
	hash := sha256.Sum256(content)
	rawID, err := id.New("art")
	if err != nil {
		return digest.Artifact{}, err
	}
	now := time.Now().UTC()
	artifact := digest.Artifact{ID: domain.ArtifactID(rawID), MissionID: missionID, Kind: "mission.digest",
		MediaType: "text/markdown", SHA256: hex.EncodeToString(hash[:]), Content: string(content),
		CreatedBy: actor, CreatedAt: now}
	if len(events) > 0 {
		artifact.BasedOnEventSequence = events[len(events)-1].Sequence
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_digest_artifacts(id, mission_id, kind, media_type,
		sha256, content, based_on_event_sequence, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifact.ID, artifact.MissionID, artifact.Kind, artifact.MediaType, artifact.SHA256, artifact.Content,
		artifact.BasedOnEventSequence, artifact.CreatedBy, formatTime(now)); err != nil {
		return digest.Artifact{}, fmt.Errorf("insert mission digest artifact: %w", err)
	}
	if err := appendEvent(ctx, tx, eventInput{MissionID: &missionID, Actor: actor, Type: "mission.digest_regenerated",
		CommandID: commandID, Payload: map[string]any{"artifact_id": artifact.ID, "sha256": artifact.SHA256,
			"based_on_event_sequence": artifact.BasedOnEventSequence, "reason": reason}}); err != nil {
		return digest.Artifact{}, err
	}
	return artifact, nil
}

const missionDigestSelect = `SELECT id, mission_id, kind, media_type, sha256, content,
	based_on_event_sequence, created_by, created_at FROM mission_digest_artifacts`

func scanMissionDigest(row rowScanner) (digest.Artifact, error) {
	var artifact digest.Artifact
	var created string
	if err := row.Scan(&artifact.ID, &artifact.MissionID, &artifact.Kind, &artifact.MediaType,
		&artifact.SHA256, &artifact.Content, &artifact.BasedOnEventSequence, &artifact.CreatedBy,
		&created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return digest.Artifact{}, ErrNotFound
		}
		return digest.Artifact{}, err
	}
	parsed, err := parseTime(created)
	if err != nil {
		return digest.Artifact{}, err
	}
	artifact.CreatedAt = parsed
	return artifact, nil
}

func tasksTx(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) ([]domain.Task, error) {
	rows, err := tx.QueryContext(ctx, taskSelectMany+" WHERE mission_id = ? ORDER BY priority DESC, created_at, id", missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Task, 0)
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func signalsTx(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) ([]signalpolicy.Signal, error) {
	rows, err := tx.QueryContext(ctx, signalSelect+" WHERE mission_id = ? ORDER BY created_at, id", missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]signalpolicy.Signal, 0)
	for rows.Next() {
		item, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func eventsTx(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) ([]domain.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT sequence, mission_id, task_id, actor, type, command_id,
		payload_json, created_at FROM events WHERE mission_id = ? ORDER BY sequence`, missionID)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}
