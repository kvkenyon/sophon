package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"parallel-intellect/internal/db"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/treehouse"
)

func main() {
	path := flag.String("db", "", "SQLite database path")
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
	<-ctx.Done()
	log.Print("pintellectd stopped")
}
