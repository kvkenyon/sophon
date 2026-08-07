package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
)

const waitPollInterval = 100 * time.Millisecond

var errWaitTimeout = errors.New("wait timed out with no new events")

// waitCommand blocks until the mission event stream advances beyond after-seq.
// A zero timeout waits until an event arrives or the command is interrupted.
func waitCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	mission := flags.String("mission", "", "mission ID")
	timeout := flags.Duration("timeout", 0, "maximum time to wait (zero waits indefinitely)")
	afterSequence := flags.Int64("after-seq", 0, "return only events after this sequence")
	dbPath := flags.String("db", "", "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("wait does not accept positional arguments")
	}
	if *mission == "" {
		return errors.New("wait requires --mission")
	}
	if *timeout < 0 {
		return errors.New("wait timeout must not be negative")
	}

	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	events, err := waitForMissionEvents(ctx, store, domain.MissionID(*mission), *afterSequence, *timeout)
	if err != nil {
		if errors.Is(err, errWaitTimeout) {
			return &exitError{code: 2, err: err}
		}
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(events)
}

func waitForMissionEvents(ctx context.Context, store *db.Store, missionID domain.MissionID, afterSequence int64, timeout time.Duration) ([]domain.Event, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		events, err := store.MissionEventsAfter(ctx, missionID, afterSequence)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			return events, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return nil, errWaitTimeout
		}

		delay := waitPollInterval
		if !deadline.IsZero() {
			if remaining := time.Until(deadline); remaining < delay {
				delay = remaining
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("wait for mission events: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
