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
	reconcileCommanders()
	ticker := time.NewTicker(*commanderPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("pintellectd stopped")
			return
		case <-ticker.C:
			reconcileCommanders()
		}
	}
}
