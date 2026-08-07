package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	commandercontrol "parallel-intellect/internal/commander"
	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	"parallel-intellect/internal/herdr"
)

func homeCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("home", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	agent := flags.String("agent", "codex", "commander runtime: codex or claude")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "", "explicit Herdr session override")
	promptDir := flags.String("prompt-dir", "", "commander prompt directory override")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("home does not accept positional arguments")
	}
	paths, err := currentDaemonPaths()
	if err != nil {
		return err
	}
	if _, running := daemonPID(paths); !running {
		fmt.Println("Warning: pintellectd is not running; wake routing is unavailable. Start it with: pintellect daemon start")
	}

	operatorSession, err := detectHerdrSession(ctx, *herdrBinary, strings.TrimSpace(*herdrSession), os.Getenv)
	if err != nil {
		return err
	}
	projectRoot, err := projectRootFromCWD(ctx)
	if err != nil {
		return err
	}

	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	project, err := ensureHomeProject(ctx, store, projectRoot)
	if err != nil {
		return err
	}
	workspaceLabel := project.Name + " · commander"

	session, err := store.ProjectCommanderSession(ctx, project.ID)
	if errors.Is(err, db.ErrNotFound) {
		session, err = startProjectCommander(ctx, store, project, homeStartOptions{
			Agent: *agent, DatabasePath: *dbPath, HerdrBinary: *herdrBinary,
			HerdrSession: operatorSession, HerdrWorkspace: workspaceLabel, PromptDir: *promptDir,
		})
		if err != nil {
			return err
		}
		if session.MissionID == "" {
			fmt.Printf("Commander ready for %s in intake mode.\n", project.Name)
		} else {
			fmt.Printf("Commander ready to resume mission %s for %s.\n", session.MissionID, project.Name)
		}
	} else if err != nil {
		return err
	} else {
		terminal := herdr.NewCommandAdapter(*herdrBinary, session.HerdrSessionName, workspaceLabel)
		session, err = (&commandercontrol.Reconciler{
			Store: store, Runtime: commandercontrol.HerdrAdapter{Terminal: terminal},
		}).ReconcileProject(ctx, project.ID)
		if err != nil {
			return fmt.Errorf("resume project commander: %w", err)
		}
		if session.State == domain.CommanderSessionNeedsAttention {
			installDir, installErr := binaryInstallDir()
			if installErr != nil {
				return installErr
			}
			session, err = (&commandercontrol.Recovery{Store: store, Runtime: commandercontrol.HerdrAdapter{Terminal: terminal},
				Prompts: commandercontrol.PromptComposer{Dir: *promptDir, InstallDir: installDir}, DatabasePath: *dbPath}).RecoverProject(ctx, project.ID)
			if err != nil {
				return fmt.Errorf("recover project commander: %w", err)
			}
			fmt.Printf("Commander recovered for %s.\n", project.Name)
		}
		if session.State == domain.CommanderSessionStopped || session.State == domain.CommanderSessionFailed {
			return fmt.Errorf("project commander %s requires attention (%s): %s", session.ID, session.State, session.FailureReason)
		}
		if session.HerdrSessionName != operatorSession {
			fmt.Printf("Commander is persisted in Herdr session %s; attaching there from %s.\n", session.HerdrSessionName, operatorSession)
		}
	}
	return attachHomeCommander(ctx, *herdrBinary, session)
}

type listedHerdrSession struct {
	Name       string `json:"name"`
	Running    bool   `json:"running"`
	SocketPath string `json:"socket_path"`
}

func detectHerdrSession(ctx context.Context, binary, explicit string, getenv func(string) string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		binary = "herdr"
	}
	output, err := exec.CommandContext(ctx, binary, "session", "list", "--json").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("detect current Herdr session: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var response struct {
		Sessions []listedHerdrSession `json:"sessions"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", fmt.Errorf("decode Herdr session list: %w", err)
	}
	running := make([]listedHerdrSession, 0, len(response.Sessions))
	for _, session := range response.Sessions {
		if session.Running {
			running = append(running, session)
		}
	}
	requested := strings.TrimSpace(explicit)
	if requested == "" {
		requested = strings.TrimSpace(getenv("HERDR_SESSION"))
	}
	if requested != "" {
		for _, session := range running {
			if session.Name == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("Herdr session %q from the environment is not running", requested)
	}

	socketCandidates := make(map[string]struct{})
	for _, key := range []string{"HERDR_SOCKET_PATH", "HERDR_CLIENT_SOCKET_PATH"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			socketCandidates[value] = struct{}{}
		}
	}
	if len(socketCandidates) != 0 {
		matches := make(map[string]struct{})
		for _, session := range running {
			if _, ok := socketCandidates[session.SocketPath]; ok {
				matches[session.Name] = struct{}{}
			}
		}
		if len(matches) == 1 {
			for name := range matches {
				return name, nil
			}
		}
		if len(matches) > 1 {
			return "", errors.New("Herdr environment identifies multiple running sessions")
		}
	}
	if len(running) == 1 {
		return running[0].Name, nil
	}
	if len(running) == 0 {
		return "", errors.New("no running Herdr session was detected; run pintellect home from inside Herdr")
	}
	names := make([]string, 0, len(running))
	for _, session := range running {
		names = append(names, session.Name)
	}
	return "", fmt.Errorf("cannot detect the current Herdr session: multiple sessions are running (%s); run from inside Herdr or set HERDR_SESSION", strings.Join(names, ", "))
}

func projectRootFromCWD(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("read current directory: %w", err)
	}
	output, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", errors.New("the current directory is not inside a Git repository")
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", errors.New("Git did not report a repository root for the current directory")
	}
	return filepath.Clean(root), nil
}

func ensureHomeProject(ctx context.Context, store *db.Store, root string) (domain.Project, error) {
	projectID, err := store.ProjectByPath(ctx, root)
	if errors.Is(err, db.ErrNotFound) {
		projects, listErr := store.Projects(ctx)
		if listErr != nil {
			return domain.Project{}, listErr
		}
		name := uniqueHomeProjectName(root, projects)
		command, commandErr := commandID()
		if commandErr != nil {
			return domain.Project{}, commandErr
		}
		projectID, err = store.CreateProject(ctx, command, db.CreateProjectInput{Name: name, Path: root})
	}
	if err != nil {
		return domain.Project{}, err
	}
	return store.Project(ctx, string(projectID))
}

func uniqueHomeProjectName(root string, projects []domain.Project) string {
	used := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		used[project.Name] = struct{}{}
	}
	base := filepath.Base(root)
	if _, exists := used[base]; !exists {
		return base
	}
	candidate := base + "-" + filepath.Base(filepath.Dir(root))
	if _, exists := used[candidate]; !exists {
		return candidate
	}
	for suffix := 2; ; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

type homeStartOptions struct {
	Agent          string
	DatabasePath   string
	HerdrBinary    string
	HerdrSession   string
	HerdrWorkspace string
	PromptDir      string
}

func startProjectCommander(ctx context.Context, store *db.Store, project domain.Project, options homeStartOptions) (domain.CommanderSession, error) {
	runtime := herdr.Runtime(strings.TrimSpace(options.Agent))
	if runtime != herdr.RuntimeCodex && runtime != herdr.RuntimeClaude {
		return domain.CommanderSession{}, errors.New("home supports --agent codex|claude")
	}
	if strings.TrimSpace(options.HerdrSession) == "" {
		return domain.CommanderSession{}, errors.New("home could not resolve a Herdr session")
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
	active, err := store.ActiveProjectMissions(ctx, project.ID)
	if err != nil {
		return domain.CommanderSession{}, err
	}
	if len(active) > 1 {
		return domain.CommanderSession{}, fmt.Errorf("project %s has multiple active missions; resolve them before opening home", project.Name)
	}
	if len(active) == 1 {
		started, err := starter.Start(ctx, commandercontrol.StartRequest{
			MissionID: active[0].ID, Runtime: runtime,
			Budget: domain.CommanderBudget{MaxTurns: 30, MaxDuration: 45 * time.Minute},
		})
		if err != nil {
			return domain.CommanderSession{}, err
		}
		return started.Session, nil
	}
	started, err := starter.StartProject(ctx, commandercontrol.ProjectStartRequest{
		ProjectID: project.ID, Runtime: runtime, DatabasePath: options.DatabasePath,
		Budget: domain.CommanderBudget{MaxTurns: 30, MaxDuration: 45 * time.Minute},
	})
	if err != nil {
		return domain.CommanderSession{}, err
	}
	return started.Session, nil
}

func attachHomeCommander(ctx context.Context, herdrBinary string, session domain.CommanderSession) error {
	focusArguments := []string{"agent", "focus", session.HerdrPaneID, "--session", session.HerdrSessionName}
	if output, err := exec.CommandContext(ctx, herdrBinary, focusArguments...).CombinedOutput(); err != nil {
		return fmt.Errorf("focus commander: %w: %s", err, strings.TrimSpace(string(output)))
	}
	arguments := []string{"agent", "attach", session.HerdrPaneID, "--session", session.HerdrSessionName}
	fmt.Printf("Attaching to %s commander.\n", filepath.Base(session.HerdrPaneID))
	command := exec.CommandContext(ctx, herdrBinary, arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("attach commander: %w", err)
	}
	return nil
}
