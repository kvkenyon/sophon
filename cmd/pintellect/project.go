package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"parallel-intellect/internal/db"
)

func project(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected: pintellect project add|list|inspect")
	}
	switch args[0] {
	case "add":
		return projectAdd(ctx, args[1:])
	case "list":
		return projectList(ctx, args[1:])
	case "inspect":
		return projectInspect(ctx, args[1:])
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func projectAdd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("project add", flag.ContinueOnError)
	path := flags.String("path", "", "project path")
	name := flags.String("name", "", "project name")
	dbPath := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("project add accepts one path")
	}
	if *path == "" && flags.NArg() == 1 {
		*path = flags.Arg(0)
	}
	if strings.TrimSpace(*path) == "" {
		return errors.New("project add requires PATH")
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	if *name == "" {
		*name = filepath.Base(absolute)
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
	id, err := store.CreateProject(ctx, command, db.CreateProjectInput{Name: *name, Path: absolute})
	if err != nil {
		return err
	}
	item, err := store.Project(ctx, string(id))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(item)
	}
	fmt.Printf("%s\t%s\t%s\n", item.ID, item.Name, item.Path)
	return nil
}

func projectList(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("project list", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("project list does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	items, err := store.Projects(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(items)
	}
	for _, item := range items {
		fmt.Printf("%s\t%s\t%s\n", item.ID, item.Name, item.Path)
	}
	return nil
}

func projectInspect(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("expected: pintellect project inspect NAME [--db PATH] [--json]")
	}
	flags := flag.NewFlagSet("project inspect", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("project inspect accepts exactly one name")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	item, err := store.Project(ctx, args[0])
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(item)
	}
	fmt.Printf("ID:\t%s\nName:\t%s\nPath:\t%s\n", item.ID, item.Name, item.Path)
	return nil
}
