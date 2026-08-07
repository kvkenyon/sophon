package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	commandercontrol "parallel-intellect/internal/commander"
	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/herdr"
	"parallel-intellect/internal/treehouse"
)

func main() {
	path := flag.String("db", "", "SQLite database path")
	herdrBinary := flag.String("herdr", "herdr", "Herdr CLI binary")
	commanderPoll := flag.Duration("commander-poll", time.Second, "commander reconciliation and event wake interval")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := db.Open(ctx, *path)
	if err != nil {
		log.Fatal(fmt.Errorf("open control-plane database: %w", err))
	}
	defer store.Close()

	leaseService := treehouse.NewService(store, treehouse.NewCommandClient("treehouse"), gitcontrol.NewClient())
	reconciled, err := leaseService.Reconcile(ctx)
	if err != nil {
		log.Fatal(fmt.Errorf("reconcile Treehouse leases: %w", err))
	}
	log.Printf("pintellectd initialized database %s; Treehouse leases valid=%d fenced=%d missing=%d",
		*path, reconciled.Valid, reconciled.Fenced, reconciled.Missing)
	enforceBudgets := func() {
		missions, err := store.Missions(ctx)
		if err != nil {
			log.Printf("list missions for budget enforcement: %v", err)
			return
		}
		now := time.Now().UTC()
		for _, mission := range missions {
			if mission.Budget.MaxWallClock > 0 && now.Sub(mission.CreatedAt) >= mission.Budget.MaxWallClock {
				_, err = store.EnforceMissionBudget(ctx,
					domain.CommandID("cmd_budget_mission_wall:"+string(mission.ID)), db.EnforceMissionBudgetInput{
						MissionID: mission.ID, ObservedAt: mission.CreatedAt.Add(mission.Budget.MaxWallClock), Actor: "budget-enforcer",
					})
				if err != nil {
					log.Printf("enforce mission budget mission=%s: %v", mission.ID, err)
				}
			}
			workers, workerErr := store.WorkerSessions(ctx, mission.ID)
			if workerErr != nil {
				log.Printf("list workers for budget enforcement mission=%s: %v", mission.ID, workerErr)
				continue
			}
			for _, session := range workers {
				if session.Budget.MaxRuntime <= 0 || now.Sub(session.CreatedAt) < session.Budget.MaxRuntime {
					continue
				}
				task, taskErr := store.Task(ctx, session.TaskID)
				if taskErr != nil || task.CurrentAttempt != session.Attempt || task.State == "needs_attention" {
					continue
				}
				_, workerErr = store.ReserveWorkerBudget(ctx,
					domain.CommandID("cmd_budget_worker_runtime:"+string(session.ID)), db.ReserveWorkerBudgetInput{
						TaskID: task.ID, Attempt: session.Attempt, SessionID: session.ID,
						ExpectedVersion: session.Version, Dimension: "runtime",
						ObservedAt: session.CreatedAt.Add(session.Budget.MaxRuntime), Actor: "budget-enforcer",
					})
				if workerErr != nil {
					log.Printf("enforce worker budget task=%s session=%s: %v", task.ID, session.ID, workerErr)
				}
			}
		}
		sessions, err := store.CommanderSessions(ctx)
		if err != nil {
			log.Printf("list commanders for budget enforcement: %v", err)
			return
		}
		for _, session := range sessions {
			if session.State == "needs_attention" || session.State == "failed" || session.State == "stopped" ||
				session.Budget.MaxDuration <= 0 || now.Sub(session.CreatedAt) < session.Budget.MaxDuration {
				continue
			}
			_, err = store.ReserveCommanderTurn(ctx,
				domain.CommandID("cmd_budget_commander_duration:"+string(session.ID)), db.ReserveCommanderTurnInput{
					MissionID: session.MissionID, SessionID: session.ID, ExpectedVersion: session.Version,
					ObservedAt: session.CreatedAt.Add(session.Budget.MaxDuration), Actor: "budget-enforcer",
				})
			if err != nil {
				log.Printf("enforce commander budget mission=%s session=%s: %v", session.MissionID, session.ID, err)
			}
		}
	}

	reconcileCommanders := func() {
		sessions, err := store.CommanderSessions(ctx)
		if err != nil {
			log.Printf("list commander sessions: %v", err)
			return
		}
		for _, session := range sessions {
			terminal := herdr.NewCommandAdapter(*herdrBinary, session.HerdrSessionName, "")
			runtime := commandercontrol.HerdrAdapter{Terminal: terminal}
			current, err := (&commandercontrol.Reconciler{Store: store, Runtime: runtime}).Reconcile(ctx, session.MissionID)
			if err != nil {
				log.Printf("reconcile commander mission=%s session=%s: %v", session.MissionID, session.ID, err)
				continue
			}
			if current.State == "needs_attention" || current.State == "failed" || current.State == "stopped" {
				continue
			}
			if _, err := (&commandercontrol.EventWaker{Store: store, Runtime: runtime}).Wake(ctx, session.MissionID); err != nil {
				log.Printf("wake commander mission=%s session=%s: %v", session.MissionID, session.ID, err)
			}
		}
	}
	enforceBudgets()
	reconcileCommanders()
	ticker := time.NewTicker(*commanderPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("pintellectd stopped")
			return
		case <-ticker.C:
			enforceBudgets()
			reconcileCommanders()
		}
	}
}
