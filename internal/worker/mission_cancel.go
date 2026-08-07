package worker

import (
	"context"
	"errors"

	"sophon/internal/db"
	"sophon/internal/domain"
)

// MissionCanceller coordinates the durable mission fence with the existing
// task cancellation path, preserving its conditional lease cleanup semantics.
type MissionCanceller struct {
	Store *db.Store
	Tasks *Canceller
}

func (c *MissionCanceller) Cancel(ctx context.Context, missionID domain.MissionID, commandID domain.CommandID) (domain.Mission, error) {
	if c == nil || c.Store == nil || c.Tasks == nil || missionID == "" || commandID == "" {
		return domain.Mission{}, errors.New("mission canceller is not fully configured")
	}
	if _, err := c.Store.BeginMissionCancel(ctx, commandID+"_begin", missionID, "operator"); err != nil {
		return domain.Mission{}, err
	}
	tasks, err := c.Store.Tasks(ctx, missionID)
	if err != nil {
		return domain.Mission{}, err
	}
	for _, task := range tasks {
		if !isTerminal(task.State) {
			if _, err := c.Tasks.Cancel(ctx, task.ID, commandID+"_task_"+domain.CommandID(task.ID)); err != nil {
				return domain.Mission{}, err
			}
		}
	}
	return c.Store.FinishMissionCancel(ctx, commandID, missionID, "operator")
}
