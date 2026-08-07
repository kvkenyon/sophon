package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"parallel-intellect/internal/domain"
)

type eventInput struct {
	MissionID *domain.MissionID
	TaskID    *domain.TaskID
	Actor     string
	Type      string
	CommandID *domain.CommandID
	Payload   any
}

func appendEvent(ctx context.Context, tx *sql.Tx, in eventInput) error {
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return fmt.Errorf("encode event payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(
		mission_id, task_id, actor, type, command_id, payload_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`, missionIDValue(in.MissionID), taskIDValue(in.TaskID), in.Actor,
		in.Type, commandIDValue(in.CommandID), payload, formatTime(time.Now())); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (s *Store) TaskEvents(ctx context.Context, taskID domain.TaskID) ([]domain.Event, error) {
	return s.events(ctx, "task_id", taskID)
}

func (s *Store) MissionEvents(ctx context.Context, missionID domain.MissionID) ([]domain.Event, error) {
	return s.events(ctx, "mission_id", missionID)
}

func (s *Store) MissionEventsAfter(ctx context.Context, missionID domain.MissionID, sequence int64) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, mission_id, task_id, actor, type, command_id,
		payload_json, created_at FROM events WHERE mission_id = ? AND sequence > ? ORDER BY sequence`, missionID, sequence)
	if err != nil {
		return nil, fmt.Errorf("query mission events after cursor: %w", err)
	}
	return scanEvents(rows)
}

func (s *Store) RecentMissionEvents(ctx context.Context, missionID domain.MissionID, limit int) ([]domain.Event, error) {
	if limit < 1 {
		return []domain.Event{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, mission_id, task_id, actor, type, command_id,
		payload_json, created_at FROM (SELECT sequence, mission_id, task_id, actor, type, command_id,
		payload_json, created_at FROM events WHERE mission_id = ? ORDER BY sequence DESC LIMIT ?)
		ORDER BY sequence`, missionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent mission events: %w", err)
	}
	return scanEvents(rows)
}

func (s *Store) events(ctx context.Context, column string, id any) ([]domain.Event, error) {
	if column != "task_id" && column != "mission_id" {
		return nil, errors.New("unsupported event scope")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, mission_id, task_id, actor, type, command_id,
		payload_json, created_at FROM events WHERE `+column+` = ? ORDER BY sequence`, id)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]domain.Event, error) {
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var event domain.Event
		var missionID, taskID, commandID sql.NullString
		var payload []byte
		var created string
		if err := rows.Scan(&event.Sequence, &missionID, &taskID, &event.Actor, &event.Type,
			&commandID, &payload, &created); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if missionID.Valid {
			value := domain.MissionID(missionID.String)
			event.MissionID = &value
		}
		if taskID.Valid {
			value := domain.TaskID(taskID.String)
			event.TaskID = &value
		}
		if commandID.Valid {
			value := domain.CommandID(commandID.String)
			event.CommandID = &value
		}
		event.Payload = json.RawMessage(payload)
		parsed, err := parseTime(created)
		if err != nil {
			return nil, fmt.Errorf("parse event timestamp: %w", err)
		}
		event.CreatedAt = parsed
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

func missionIDValue(value *domain.MissionID) any {
	if value == nil {
		return nil
	}
	return *value
}

func commandIDValue(value *domain.CommandID) any {
	if value == nil {
		return nil
	}
	return *value
}
