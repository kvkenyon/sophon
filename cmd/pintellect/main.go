package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
)

const version = "0.1.0-m1"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pintellect:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
		return nil
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		path := flags.String("db", "parallel-intellect.db", "SQLite database path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if dir := filepath.Dir(*path); dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("create database directory: %w", err)
			}
		}
		store, err := db.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		fmt.Println(*path)
		return nil
	case "task", "mission":
		return timeline(ctx, args)
	case "commander":
		return commander(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func commander(args []string) error {
	if len(args) == 0 {
		return errors.New("expected: pintellect commander start|attach|status")
	}
	switch args[0] {
	case "start":
		flags := flag.NewFlagSet("commander start", flag.ContinueOnError)
		agent := flags.String("agent", "prime", "commander runtime")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return fmt.Errorf("commander start --agent %s is reserved for the runtime milestone and is not enabled in milestone 1", *agent)
	case "attach", "status":
		if len(args) != 1 {
			return fmt.Errorf("commander %s does not accept arguments in milestone 1", args[0])
		}
		return fmt.Errorf("commander %s is reserved for the runtime milestone and is not enabled in milestone 1", args[0])
	default:
		return fmt.Errorf("unknown commander command %q", args[0])
	}
}

func timeline(ctx context.Context, args []string) error {
	if len(args) < 3 || args[1] != "timeline" {
		return errors.New("expected: pintellect task|mission timeline <id> [--db PATH]")
	}
	flags := flag.NewFlagSet("timeline", flag.ContinueOnError)
	path := flags.String("db", "parallel-intellect.db", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[3:]); err != nil {
		return err
	}
	store, err := db.Open(ctx, *path)
	if err != nil {
		return err
	}
	defer store.Close()
	var events []domain.Event
	if args[0] == "task" {
		events, err = store.TaskEvents(ctx, domain.TaskID(args[2]))
	} else {
		events, err = store.MissionEvents(ctx, domain.MissionID(args[2]))
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(events)
	}
	for _, event := range events {
		fmt.Printf("%d\t%s\t%s\t%s\n", event.Sequence, event.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"), event.Actor, event.Type)
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  pintellect init [--db PATH]
  pintellect task timeline <id> [--db PATH] [--json]
  pintellect mission timeline <id> [--db PATH] [--json]
  pintellect commander start|attach|status
  pintellect version`)
}
