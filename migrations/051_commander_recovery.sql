-- Retired commander placements remain auditable while a project or mission
-- may receive a new active placement after front-door recovery.
DROP INDEX commander_sessions_mission_idx;

CREATE UNIQUE INDEX commander_sessions_active_project_idx
    ON commander_sessions(project_id)
    WHERE state NOT IN ('stopped', 'failed');
CREATE UNIQUE INDEX commander_sessions_active_mission_idx
    ON commander_sessions(mission_id)
    WHERE mission_id IS NOT NULL AND state NOT IN ('stopped', 'failed');
