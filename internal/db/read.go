package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sophon/internal/domain"
)

const missionSelect = `SELECT id, project_id, commander_session_id, title, objective, acceptance_criteria_json,
	state, version, max_wall_clock_ns, max_concurrent_tasks, max_task_attempts, max_validation_runs,
	max_tokens, max_cost, created_at, completed_at FROM missions`

// Mission returns the durable mission projection.
func (s *Store) Mission(ctx context.Context, missionID domain.MissionID) (domain.Mission, error) {
	return scanMission(s.db.QueryRowContext(ctx, missionSelect+" WHERE id = ?", missionID))
}

// Missions returns all missions in deterministic creation order.
func (s *Store) Missions(ctx context.Context) ([]domain.Mission, error) {
	rows, err := s.db.QueryContext(ctx, missionSelect+" ORDER BY created_at, id")
	if err != nil {
		return nil, fmt.Errorf("list missions: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Mission, 0)
	for rows.Next() {
		item, err := scanMission(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate missions: %w", err)
	}
	return items, nil
}

// ActiveProjectMissions returns the resumable missions for one registered
// project. Home uses this only when no persistent project commander exists.
func (s *Store) ActiveProjectMissions(ctx context.Context, projectID domain.ProjectID) ([]domain.Mission, error) {
	rows, err := s.db.QueryContext(ctx, missionSelect+" WHERE project_id = ? AND state IN ('active', 'completing', 'cancelling') ORDER BY created_at, id", projectID)
	if err != nil {
		return nil, fmt.Errorf("list active project missions: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Mission, 0)
	for rows.Next() {
		item, err := scanMission(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active project missions: %w", err)
	}
	return items, nil
}

func scanMission(row rowScanner) (domain.Mission, error) {
	var mission domain.Mission
	var commander, criteria, maxCost, created, completed sql.NullString
	var maxTokens sql.NullInt64
	var maxWallClock int64
	if err := row.Scan(&mission.ID, &mission.ProjectID, &commander, &mission.Title, &mission.Objective, &criteria,
		&mission.State, &mission.Version, &maxWallClock, &mission.Budget.MaxConcurrentTasks,
		&mission.Budget.MaxTaskAttempts, &mission.Budget.MaxValidationRuns, &maxTokens, &maxCost, &created, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Mission{}, ErrNotFound
		}
		return domain.Mission{}, fmt.Errorf("scan mission: %w", err)
	}
	if commander.Valid {
		mission.CommanderSessionID = domain.SessionID(commander.String)
	}
	if maxTokens.Valid {
		value := maxTokens.Int64
		mission.Budget.MaxTokens = &value
	}
	if maxCost.Valid {
		value := maxCost.String
		mission.Budget.MaxCost = &value
	}
	mission.Budget.MaxWallClock = time.Duration(maxWallClock)
	if err := json.Unmarshal([]byte(criteria.String), &mission.AcceptanceCriteria); err != nil {
		return domain.Mission{}, fmt.Errorf("decode mission criteria: %w", err)
	}
	var err error
	mission.CreatedAt, err = parseTime(created.String)
	if err == nil && completed.Valid {
		mission.CompletedAt, err = parseOptionalTime(&completed.String)
	}
	if err != nil {
		return domain.Mission{}, fmt.Errorf("parse mission time: %w", err)
	}
	return mission, nil
}

// Tasks returns tasks in a mission in stable priority and creation order.
func (s *Store) Tasks(ctx context.Context, missionID domain.MissionID) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, taskSelectMany+" WHERE mission_id = ? ORDER BY priority DESC, created_at, id", missionID)
	if err != nil {
		return nil, fmt.Errorf("list mission tasks: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mission tasks: %w", err)
	}
	return items, nil
}

// NonterminalTasks returns every task that startup reconciliation may need to
// observe. The explicit state list keeps recovery aligned with task policy
// without treating needs_attention as terminal or inferring from timestamps.
func (s *Store) NonterminalTasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, taskSelectMany+` WHERE state NOT IN (
		'delivered', 'delivered_branch', 'report_ready', 'cancelled', 'failed'
	) ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list nonterminal tasks: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nonterminal tasks: %w", err)
	}
	return items, nil
}

// WorkerSessions returns all durable worker placements for a mission.
func (s *Store) WorkerSessions(ctx context.Context, missionID domain.MissionID) ([]domain.WorkerSession, error) {
	rows, err := s.db.QueryContext(ctx, workerSessionSelect+` WHERE task_id IN (SELECT id FROM tasks WHERE mission_id = ?) ORDER BY task_id, attempt`, missionID)
	if err != nil {
		return nil, fmt.Errorf("list mission worker sessions: %w", err)
	}
	defer rows.Close()
	items := make([]domain.WorkerSession, 0)
	for rows.Next() {
		item, err := scanWorkerSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mission worker sessions: %w", err)
	}
	return items, nil
}

// Projects returns registered projects in deterministic name order.
func (s *Store) Projects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, path, created_at FROM projects ORDER BY name, id")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Project, 0)
	for rows.Next() {
		item, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return items, nil
}

// Project returns one project by its stable display name or identifier.
func (s *Store) Project(ctx context.Context, nameOrID string) (domain.Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, "SELECT id, name, path, created_at FROM projects WHERE name = ? OR id = ?", nameOrID, nameOrID))
}

func scanProject(row rowScanner) (domain.Project, error) {
	var project domain.Project
	var created string
	if err := row.Scan(&project.ID, &project.Name, &project.Path, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Project{}, ErrNotFound
		}
		return domain.Project{}, fmt.Errorf("scan project: %w", err)
	}
	var err error
	project.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.Project{}, fmt.Errorf("parse project time: %w", err)
	}
	return project, nil
}
