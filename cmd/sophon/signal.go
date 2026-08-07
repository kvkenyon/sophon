package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"sophon/internal/db"
	"sophon/internal/domain"
	"sophon/internal/id"
	"sophon/internal/signals"
)

func signalCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected: sophon signal raise|list|inspect|resolve")
	}
	switch args[0] {
	case "raise":
		flags := flag.NewFlagSet("signal raise", flag.ContinueOnError)
		path := flags.String("db", "", "SQLite database path")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		missionID := flags.String("mission", "", "mission ID")
		taskID := flags.String("task", "", "optional task ID")
		kind := flags.String("kind", string(signals.SignalDecision), "signal kind")
		question := flags.String("question", "", "operator question")
		contextText := flags.String("context", "", "supporting context")
		recommendation := flags.String("recommendation", "", "recommended answer")
		commandID := flags.String("command-id", "", "idempotency key")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("signal raise does not accept positional arguments")
		}
		store, err := db.Open(ctx, *path)
		if err != nil {
			return err
		}
		defer store.Close()
		command := domain.CommandID(*commandID)
		if command == "" {
			raw, err := id.New("cmd")
			if err != nil {
				return err
			}
			command = domain.CommandID(raw)
		}
		var task *domain.TaskID
		if *taskID != "" {
			value := domain.TaskID(*taskID)
			task = &value
		}
		item, err := store.CreateSignal(ctx, command, db.CreateSignalInput{MissionID: domain.MissionID(*missionID), TaskID: task, Kind: signals.SignalKind(*kind), Question: *question, Context: *contextText, Recommendation: *recommendation, Actor: "commander"})
		if err != nil {
			return err
		}
		if *jsonOutput {
			return encode(item)
		}
		fmt.Printf("%s\t%s\t%s\n", item.ID, item.Status, item.Question)
		return nil
	case "list":
		flags := flag.NewFlagSet("signal list", flag.ContinueOnError)
		path := flags.String("db", "", "SQLite database path")
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
			return errors.New("expected: sophon signal inspect <id> [--db PATH] [--json]")
		}
		flags := flag.NewFlagSet("signal inspect", flag.ContinueOnError)
		path := flags.String("db", "", "SQLite database path")
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
			return errors.New("expected: sophon signal resolve <id> --answer ANSWER [--db PATH] [--json]")
		}
		flags := flag.NewFlagSet("signal resolve", flag.ContinueOnError)
		path := flags.String("db", "", "SQLite database path")
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
