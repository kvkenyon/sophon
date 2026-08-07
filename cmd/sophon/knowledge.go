package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"sophon/internal/db"
	"sophon/internal/knowledge"
)

func knowledgeCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected: sophon knowledge list|promote|reject|supersede")
	}
	switch args[0] {
	case "list":
		return knowledgeList(ctx, args[1:])
	case "promote":
		return knowledgeTransition(ctx, args[1:], knowledge.StatusActive)
	case "reject":
		return knowledgeTransition(ctx, args[1:], knowledge.StatusRejected)
	case "supersede":
		return knowledgeTransition(ctx, args[1:], knowledge.StatusSuperseded)
	default:
		return fmt.Errorf("unknown knowledge command %q", args[0])
	}
}

func knowledgeList(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("knowledge list", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	status := flags.String("status", "", "candidate or active")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("knowledge list does not accept positional arguments")
	}
	filter := db.ListKnowledgeFilter{}
	if value := knowledge.Status(strings.TrimSpace(*status)); value != "" {
		if value != knowledge.StatusCandidate && value != knowledge.StatusActive {
			return errors.New("knowledge list --status must be candidate or active")
		}
		filter.Status = value
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	entries, err := store.KnowledgeEntries(ctx, filter)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(entries)
	}
	for _, entry := range entries {
		trigger, evidence := "-", "-"
		if entry.TriggerTaskID != nil {
			trigger = string(*entry.TriggerTaskID)
		}
		if entry.EvidenceArtifactID != nil {
			evidence = string(*entry.EvidenceArtifactID)
		}
		fmt.Printf("%s\t%s\t%s\t%s\tcreator=%s\ttask=%s\tevidence=%s\t%s\n",
			entry.ID, entry.Status, entry.Scope, entry.Kind, entry.CreatedBy, trigger, evidence, entry.Content)
	}
	return nil
}

func knowledgeTransition(ctx context.Context, args []string, target knowledge.Status) error {
	verb := map[knowledge.Status]string{
		knowledge.StatusActive: "promote", knowledge.StatusRejected: "reject", knowledge.StatusSuperseded: "supersede",
	}[target]
	name := "knowledge " + verb
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	by := flags.String("by", "", "active replacement knowledge ID")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	var leadingID string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		leadingID, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	identifiers := append([]string(nil), flags.Args()...)
	if leadingID != "" {
		identifiers = append([]string{leadingID}, identifiers...)
	}
	if len(identifiers) != 1 {
		return fmt.Errorf("%s requires exactly one knowledge ID", name)
	}
	var replacement *knowledge.ID
	if target == knowledge.StatusSuperseded {
		value := knowledge.ID(strings.TrimSpace(*by))
		if value == "" {
			return errors.New("knowledge supersede requires --by ID")
		}
		replacement = &value
	} else if strings.TrimSpace(*by) != "" {
		return errors.New("--by is valid only for knowledge supersede")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	command, err := commandID()
	if err != nil {
		return err
	}
	updated, err := store.TransitionKnowledge(ctx, command, db.TransitionKnowledgeInput{
		KnowledgeID: knowledge.ID(identifiers[0]), To: target, Actor: "operator",
		Origin: knowledge.OriginOperator, SupersededBy: replacement,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(updated)
	}
	fmt.Printf("%s\t%s\t%s\n", updated.ID, updated.Status, updated.Content)
	return nil
}
