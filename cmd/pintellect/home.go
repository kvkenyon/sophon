package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	commandercontrol "parallel-intellect/internal/commander"
	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
	statusview "parallel-intellect/internal/status"
)

func homeCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("home", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	missionValue := flags.String("mission", "", "mission ID")
	agent := flags.String("agent", "codex", "commander runtime: codex or claude")
	yes := flags.Bool("yes", false, "start a missing commander with defaults")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "default", "Herdr session name")
	herdrWorkspace := flags.String("herdr-workspace-label", "Parallel Intellect Commander", "Herdr workspace presentation label")
	promptDir := flags.String("prompt-dir", "", "commander prompt directory override")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("home does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	missionID, ok, err := resolveHomeMission(ctx, store, domain.MissionID(strings.TrimSpace(*missionValue)))
	if err != nil {
		return err
	}
	if !ok {
		printStatus(statusview.Empty())
		fmt.Println("No missions exist. Create one with: pintellect mission create --project PATH --title TITLE --objective OBJECTIVE")
		return nil
	}
	snapshot, err := statusview.Load(ctx, store, missionID)
	if err != nil {
		return err
	}
	printStatus(snapshot)

	session, err := store.CommanderSession(ctx, missionID)
	if errors.Is(err, db.ErrNotFound) {
		approved := *yes
		if !approved {
			approved, err = confirmCommanderStart(os.Stdin, os.Stdout, *agent)
			if err != nil {
				return err
			}
		}
		if !approved {
			fmt.Printf("Commander not started. Run: pintellect home --mission %s --yes\n", missionID)
			return nil
		}
		session, err = startHomeCommander(ctx, store, homeStartOptions{
			MissionID: missionID, Agent: *agent, HerdrBinary: *herdrBinary,
			HerdrSession: *herdrSession, HerdrWorkspace: *herdrWorkspace, PromptDir: *promptDir,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Commander started: %s (%s)\n", session.ID, session.Runtime)
	} else if err != nil {
		return err
	}
	return attachHomeCommander(ctx, *herdrBinary, session)
}

func resolveHomeMission(ctx context.Context, store *db.Store, requested domain.MissionID) (domain.MissionID, bool, error) {
	if requested != "" {
		if _, err := store.Mission(ctx, requested); err != nil {
			return "", false, err
		}
		return requested, true, nil
	}
	missions, err := store.Missions(ctx)
	if err != nil {
		return "", false, err
	}
	switch len(missions) {
	case 0:
		return "", false, nil
	case 1:
		return missions[0].ID, true, nil
	default:
		ids := make([]string, 0, len(missions))
		for _, mission := range missions {
			ids = append(ids, string(mission.ID))
		}
		return "", false, fmt.Errorf("home requires --mission ID when multiple missions exist (%s)", strings.Join(ids, ", "))
	}
}

func confirmCommanderStart(input io.Reader, output io.Writer, agent string) (bool, error) {
	if _, err := fmt.Fprintf(output, "No commander session exists. Start a %s commander? [Y/n] ", agent); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read commander confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if errors.Is(err, io.EOF) && answer == "" {
		return false, errors.New("commander confirmation unavailable; rerun with --yes to accept defaults")
	}
	switch answer {
	case "", "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("expected yes or no, got %q", answer)
	}
}

type homeStartOptions struct {
	MissionID      domain.MissionID
	Agent          string
	HerdrBinary    string
	HerdrSession   string
	HerdrWorkspace string
	PromptDir      string
}

func startHomeCommander(ctx context.Context, store *db.Store, options homeStartOptions) (domain.CommanderSession, error) {
	runtime := herdr.Runtime(strings.TrimSpace(options.Agent))
	if runtime != herdr.RuntimeCodex && runtime != herdr.RuntimeClaude {
		return domain.CommanderSession{}, errors.New("home supports --agent codex|claude; Pi requires explicit commander start model configuration")
	}
	if strings.TrimSpace(options.HerdrSession) == "" {
		return domain.CommanderSession{}, errors.New("home requires an explicit --herdr-session")
	}
	installDir, err := binaryInstallDir()
	if err != nil {
		return domain.CommanderSession{}, err
	}
	terminal := herdr.NewCommandAdapter(options.HerdrBinary, options.HerdrSession, options.HerdrWorkspace)
	starter := commandercontrol.Starter{
		Store: store, Runtime: commandercontrol.HerdrAdapter{Terminal: terminal},
		Prompts: commandercontrol.PromptComposer{Dir: options.PromptDir, InstallDir: installDir},
	}
	started, err := starter.Start(ctx, commandercontrol.StartRequest{
		MissionID: options.MissionID, Runtime: runtime,
		Budget: domain.CommanderBudget{MaxTurns: 30, MaxDuration: 45 * time.Minute},
	})
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return started.Session, nil
}

func attachHomeCommander(ctx context.Context, herdrBinary string, session domain.CommanderSession) error {
	arguments := []string{"agent", "attach", session.HerdrPaneID, "--session", session.HerdrSessionName}
	fmt.Printf("Attaching: %s %s\n", herdrBinary, strings.Join(arguments, " "))
	command := exec.CommandContext(ctx, herdrBinary, arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("attach commander: %w", err)
	}
	return nil
}
