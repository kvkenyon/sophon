package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	commandercontrol "sophon/internal/commander"
	"sophon/internal/db"
	"sophon/internal/delivery"
	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/herdr"
	startup "sophon/internal/recovery"
	"sophon/internal/treehouse"
	"sophon/internal/worker"
)

const version = "0.3.0-m3"

type daemonHealth struct {
	Version          string    `json:"version"`
	StartedAt        time.Time `json:"started_at"`
	LastReconciledAt time.Time `json:"last_reconciled_at"`
	DatabasePath     string    `json:"database_path"`
}

func main() {
	path := flag.String("db", "", "SQLite database path")
	statusFile := flag.String("status-file", "", "daemon health status file")
	herdrBinary := flag.String("herdr", "herdr", "Herdr CLI binary")
	treehouseBinary := flag.String("treehouse", "treehouse", "Treehouse CLI binary")
	gitBinary := flag.String("git", "git", "Git binary")
	ghBinary := flag.String("gh-axi", "gh-axi", "gh-axi binary")
	gateBinary := flag.String("no-mistakes", "no-mistakes", "no-mistakes CLI binary")
	taskFiles := flag.String("task-files", "", "task artifact base directory")
	commanderPoll := flag.Duration("commander-poll", time.Second, "commander reconciliation and event wake interval")
	flag.Parse()
	startedAt := time.Now().UTC()
	writeHealth := func(last time.Time) {
		if *statusFile == "" {
			return
		}
		health := daemonHealth{Version: version, StartedAt: startedAt, LastReconciledAt: last, DatabasePath: *path}
		encoded, err := json.Marshal(health)
		if err != nil {
			log.Printf("encode daemon health: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Dir(*statusFile), 0o700); err != nil {
			log.Printf("create daemon health directory: %v", err)
			return
		}
		temporary := *statusFile + ".tmp"
		if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
			log.Printf("write daemon health: %v", err)
			return
		}
		if err := os.Rename(temporary, *statusFile); err != nil {
			log.Printf("replace daemon health: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := db.Open(ctx, *path)
	if err != nil {
		log.Fatal(fmt.Errorf("open control-plane database: %w", err))
	}
	defer store.Close()

	gitClient := &gitcontrol.Client{Binary: *gitBinary}
	treehouseClient := treehouse.NewCommandClient(*treehouseBinary)
	leaseService := treehouse.NewService(store, treehouseClient, gitClient)
	briefs := worker.BriefGenerator{BaseDir: *taskFiles}
	completer := &worker.Completer{Store: store, Git: gitClient, Leases: treehouseClient, TaskFiles: briefs}
	deliveryService := &delivery.Service{Store: store, Git: delivery.CommandGit{Binary: *gitBinary},
		Remote: delivery.CommandRemote{GitBinary: *gitBinary, GHBinary: *ghBinary},
		Gate:   delivery.CommandGate{Binary: *gateBinary}, Leases: leaseService}
	reconciler := &startup.Service{Store: store, Leases: leaseService,
		Worker: func(session domain.WorkerSession) startup.WorkerReconciler {
			terminal := herdr.NewCommandAdapter(*herdrBinary, session.HerdrSessionName, "")
			return &worker.Reconciler{Store: store, Herdr: terminal,
				Outcomes: worker.ResultFileInspector{TaskFiles: briefs}}
		},
		Completion: &worker.CompletionResumer{Store: store, Completer: completer, Git: gitClient},
		Delivery:   deliveryService,
	}
	reconcileTasks := func(detailed bool) {
		report, err := reconciler.Reconcile(ctx)
		if err != nil {
			log.Printf("startup reconciliation failed: %v", err)
			return
		}
		writeHealth(time.Now().UTC())
		log.Printf("reconciled leases valid=%d adopted=%d awaiting=%d released=%d fenced=%d missing=%d tasks=%d",
			report.Leases.Valid, report.Leases.Adopted, report.Leases.Awaiting,
			report.Leases.Released, report.Leases.Fenced, report.Leases.Missing, len(report.Tasks))
		for _, task := range report.Tasks {
			if task.Error != "" || (detailed && task.Outcome == startup.OutcomeRecoverable) {
				detail := task.Error
				if detail == "" {
					detail = "durable state requires scheduler or operator continuation"
				}
				log.Printf("reconcile task=%s attempt=%d state=%s status=%s outcome=%s: %s",
					task.TaskID, task.Attempt, task.State, task.Status, task.Outcome, detail)
			}
		}
	}
	reconcileTasks(true)
	log.Printf("sophond initialized database %s", *path)

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
				session.Budget.MaxDuration <= 0 || now.Sub(session.BudgetStartedAt) < session.Budget.MaxDuration {
				continue
			}
			budgetWindowID := domain.CommandID(fmt.Sprintf("cmd_budget_commander_duration:%s:%d", session.ID, session.BudgetStartedAt.UnixNano()))
			updated, err := store.ReserveCommanderTurn(ctx,
				budgetWindowID, db.ReserveCommanderTurnInput{
					MissionID: session.MissionID, SessionID: session.ID, ExpectedVersion: session.Version,
					ObservedAt: session.BudgetStartedAt.Add(session.Budget.MaxDuration), Actor: "budget-enforcer",
				})
			if err != nil {
				log.Printf("enforce commander budget mission=%s session=%s: %v", session.MissionID, session.ID, err)
			} else if updated.State == domain.CommanderSessionNeedsAttention {
				commandID := domain.CommandID(fmt.Sprintf("cmd_budget_commander_signal:%s:%d", session.ID, session.BudgetStartedAt.UnixNano()))
				if _, signalErr := store.EnsureCommanderBudgetSignal(ctx, commandID, session.ID); signalErr != nil {
					log.Printf("signal commander budget mission=%s session=%s: %v", session.MissionID, session.ID, signalErr)
				}
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
			log.Print("sophond stopped")
			return
		case <-ticker.C:
			reconcileTasks(false)
			enforceBudgets()
			reconcileCommanders()
		}
	}
}
