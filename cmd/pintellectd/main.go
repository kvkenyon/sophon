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
)

func main() {
	path := flag.String("db", "parallel-intellect.db", "SQLite database path")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := db.Open(ctx, *path)
	if err != nil {
		log.Fatal(fmt.Errorf("open control-plane database: %w", err))
	}
	defer store.Close()

	log.Printf("pintellectd initialized database %s; scheduler integrations are not enabled in milestone 1", *path)
	<-ctx.Done()
	log.Print("pintellectd stopped")
}
