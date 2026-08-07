package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	commandercontrol "parallel-intellect/internal/commander"
	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
)

func commanderCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected: pintellect commander start|renew|prompt|steer|follow-up|attach|status")
	}
	switch args[0] {
	case "start":
		return commanderStart(ctx, args[1:])
	case "renew":
		return commanderRenew(ctx, args[1:])
	case "prompt", "steer", "follow-up":
		return commanderSend(ctx, args[0], args[1:])
	case "attach":
		return commanderAttach(ctx, args[1:])
	case "status":
		return commanderStatus(ctx, args[1:])
	default:
		return fmt.Errorf("unknown commander command %q", args[0])
	}
}

func commanderStart(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("commander start", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	mission := flags.String("mission", "", "mission ID")
	agent := flags.String("agent", "", "commander runtime: pi, claude, or codex")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "", "Herdr session name (required)")
	herdrWorkspace := flags.String("herdr-workspace-label", "pintellect", "Herdr workspace presentation label")
	promptDir := flags.String("prompt-dir", "", "commander prompt directory override")
	model := flags.String("model", "", "runtime model (required for Pi)")
	piExtension := flags.String("pi-extension", "", "absolute Pi lifecycle extension path")
	maxTurns := flags.Int("max-turns", 0, "maximum commander turns (0 is unlimited)")
	maxDuration := flags.Duration("max-duration", 0, "maximum commander duration (0 is unlimited)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("commander start does not accept positional arguments")
	}
	runtime := herdr.Runtime(strings.TrimSpace(*agent))
	switch runtime {
	case herdr.RuntimeCodex, herdr.RuntimeClaude:
	case herdr.RuntimePi:
		if strings.TrimSpace(*model) == "" || strings.TrimSpace(*piExtension) == "" {
			return errors.New("Pi commander start requires --model and --pi-extension")
		}
	default:
		return errors.New("commander start requires --agent pi|claude|codex")
	}
	if strings.TrimSpace(*mission) == "" || strings.TrimSpace(*herdrSession) == "" {
		return errors.New("commander start requires --mission and an explicit --herdr-session")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	terminal := herdr.NewCommandAdapter(*herdrBinary, *herdrSession, *herdrWorkspace)
	installDir, err := binaryInstallDir()
	if err != nil {
		return err
	}
	starter := commandercontrol.Starter{Store: store, Runtime: commandercontrol.HerdrAdapter{Terminal: terminal},
		Prompts: commandercontrol.PromptComposer{Dir: *promptDir, InstallDir: installDir}}
	started, err := starter.Start(ctx, commandercontrol.StartRequest{MissionID: domain.MissionID(*mission), Runtime: runtime,
		Model: *model, PiExtensionPath: *piExtension,
		Budget: domain.CommanderBudget{MaxTurns: *maxTurns, MaxDuration: *maxDuration}})
	if err != nil {
		return err
	}
	return encode(started.Session)
}

func commanderRenew(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("commander renew", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	sessionID := flags.String("session", "", "commander session ID")
	missionID := flags.String("mission", "", "mission ID")
	maxTurns := flags.Int("max-turns", 0, "maximum commander turns (0 is unlimited when specified)")
	maxDuration := flags.Duration("max-duration", 0, "maximum commander duration (0 is unlimited when specified)")
	providedCommandID := flags.String("command-id", "", "idempotency command ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*sessionID == "" && *missionID == "") || (*sessionID != "" && *missionID != "") {
		return errors.New("commander renew requires exactly one of --session or --mission")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	var session domain.CommanderSession
	if *sessionID != "" {
		session, err = store.CommanderSessionByID(ctx, domain.SessionID(*sessionID))
	} else {
		session, err = store.CommanderSession(ctx, domain.MissionID(*missionID))
	}
	if err != nil {
		return err
	}
	budget := session.Budget
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "max-turns" {
			budget.MaxTurns = *maxTurns
		}
		if f.Name == "max-duration" {
			budget.MaxDuration = *maxDuration
		}
	})
	command := domain.CommandID(*providedCommandID)
	if command == "" {
		command, err = commandID()
		if err != nil {
			return err
		}
	}
	renewed, err := store.RenewCommanderBudget(ctx, command, db.RenewCommanderBudgetInput{SessionID: session.ID, ExpectedVersion: session.Version, Budget: budget, Actor: "operator"})
	if err != nil {
		return err
	}
	return encode(renewed)
}

func binaryInstallDir() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve pintellect executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve pintellect executable symlinks: %w", err)
	}
	return filepath.Dir(resolved), nil
}

func commanderSend(ctx context.Context, verb string, args []string) error {
	flags := flag.NewFlagSet("commander "+verb, flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	mission := flags.String("mission", "", "mission ID (optional when exactly one commander exists)")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	var leadingMessage string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		leadingMessage, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	messageParts := append([]string(nil), flags.Args()...)
	if leadingMessage != "" {
		messageParts = append([]string{leadingMessage}, messageParts...)
	}
	message := strings.TrimSpace(strings.Join(messageParts, " "))
	if message == "" {
		return fmt.Errorf("commander %s requires a message", verb)
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	missionID, session, err := resolveCommander(store, ctx, domain.MissionID(*mission))
	if err != nil {
		return err
	}
	terminal := herdr.NewCommandAdapter(*herdrBinary, session.HerdrSessionName, "")
	controller := commandercontrol.Controller{Store: store, Runtime: commandercontrol.HerdrAdapter{Terminal: terminal}}
	kind := commandercontrol.MessagePrompt
	if verb == "steer" {
		kind = commandercontrol.MessageSteer
	} else if verb == "follow-up" {
		kind = commandercontrol.MessageFollowUp
	}
	updated, err := controller.Send(ctx, missionID, kind, message)
	if err != nil {
		return err
	}
	return encode(updated)
}

func commanderAttach(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("commander attach", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	mission := flags.String("mission", "", "mission ID (optional when exactly one commander exists)")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("commander attach does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	_, session, err := resolveCommander(store, ctx, domain.MissionID(*mission))
	if err != nil {
		return err
	}
	attachment := []string{*herdrBinary, "agent", "attach", session.HerdrPaneID, "--session", session.HerdrSessionName}
	if !*jsonOutput {
		fmt.Println(strings.Join(attachment, " "))
		return nil
	}
	return encode(map[string]any{
		"commander_session": session,
		"attach":            attachment,
		"prompt":            "pintellect commander prompt --mission " + string(session.MissionID) + " <message>",
	})
}

func commanderStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("commander status", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	mission := flags.String("mission", "", "mission ID (optional when exactly one commander exists)")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("commander status does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	_, session, err := resolveCommander(store, ctx, domain.MissionID(*mission))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(session)
	}
	fmt.Printf("%s\t%s\t%s\t%s\n", session.ID, session.State, session.HerdrSessionName, session.HerdrPaneID)
	return nil
}

func resolveCommander(store *db.Store, ctx context.Context, missionID domain.MissionID) (domain.MissionID, domain.CommanderSession, error) {
	if missionID != "" {
		session, err := store.CommanderSession(ctx, missionID)
		return missionID, session, err
	}
	sessions, err := store.CommanderSessions(ctx)
	if err != nil {
		return "", domain.CommanderSession{}, err
	}
	if len(sessions) != 1 {
		return "", domain.CommanderSession{}, errors.New("--mission is required unless exactly one commander session exists")
	}
	return sessions[0].MissionID, sessions[0], nil
}
