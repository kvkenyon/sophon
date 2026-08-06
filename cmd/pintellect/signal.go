package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/id"
	"parallel-intellect/internal/signals"
)

func signalCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected: pintellect signal list|inspect|resolve")
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("signal list", flag.ContinueOnError)
		path := flags.String("db", "parallel-intellect.db", "SQLite database path")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		missionID := flags.String("mission", "", "filter by mission ID")
		status := flags.String("status", "", "filter by signal status")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("signal list does not accept positional arguments")
		}
		store, err := db.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		items, err := store.Signals(ctx, db.ListSignalsFilter{
			MissionID: domain.MissionID(*missionID), Status: signals.SignalStatus(*status),
		})
		if err != nil {
			return err
		}
		if *jsonOutput {
			return encode(items)
		}
		for _, signal := range items {
			fmt.Printf("%s\t%s\t%s\t%s\n", signal.ID, signal.Status, signal.Kind, signal.Question)
		}
		return nil
	case "inspect":
		if len(args) < 2 {
			return errors.New("expected: pintellect signal inspect <id> [--db PATH] [--json]")
		}
		flags := flag.NewFlagSet("signal inspect", flag.ContinueOnError)
		path := flags.String("db", "parallel-intellect.db", "SQLite database path")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("signal inspect accepts exactly one signal ID")
		}
		store, err := db.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		item, err := store.Signal(ctx, signals.SignalID(args[1]))
		if err != nil {
			return err
		}
		if *jsonOutput {
			return encode(item)
		}
		fmt.Printf("ID:\t%s\nMission:\t%s\nStatus:\t%s\nKind:\t%s\nQuestion:\t%s\n", item.ID, item.MissionID, item.Status, item.Kind, item.Question)
		if item.Context != "" {
			fmt.Printf("Context:\t%s\n", item.Context)
		}
		if item.Recommendation != "" {
			fmt.Printf("Recommendation:\t%s\n", item.Recommendation)
		}
		if item.Answer != nil {
			fmt.Printf("Answer:\t%s\n", *item.Answer)
		}
		return nil
	case "resolve":
		if len(args) < 2 {
			return errors.New("expected: pintellect signal resolve <id> --answer ANSWER [--db PATH] [--json]")
		}
		flags := flag.NewFlagSet("signal resolve", flag.ContinueOnError)
		path := flags.String("db", "parallel-intellect.db", "SQLite database path")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		answer := flags.String("answer", "", "operator answer")
		commandID := flags.String("command-id", "", "idempotency key")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("signal resolve accepts exactly one signal ID")
		}
		store, err := db.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		item, err := store.Signal(ctx, signals.SignalID(args[1]))
		if err != nil {
			return err
		}
		resolvedCommandID := *commandID
		if resolvedCommandID == "" {
			resolvedCommandID, err = id.New("cmd")
			if err != nil {
				return err
			}
		}
		item, err = store.ResolveSignal(ctx, domain.CommandID(resolvedCommandID), db.ResolveSignalInput{
			SignalID: item.ID, ExpectedVersion: item.Version, Answer: *answer, Actor: "operator",
		})
		if err != nil {
			return err
		}
		if *jsonOutput {
			return encode(item)
		}
		fmt.Printf("%s\t%s\t%s\n", item.ID, item.Status, *item.Answer)
		return nil
	default:
		return fmt.Errorf("unknown signal command %q", args[0])
	}
}
