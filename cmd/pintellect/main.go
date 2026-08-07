package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/delivery"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/herdr"
	"parallel-intellect/internal/id"
	statusview "parallel-intellect/internal/status"
	"parallel-intellect/internal/treehouse"
	validationcore "parallel-intellect/internal/validation"
	"parallel-intellect/internal/worker"
)

const version = "0.3.0-m3"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pintellect:", err)
		os.Exit(exitCode(err))
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
		return initialize(ctx, args[1:])
	case "mission":
		return mission(ctx, args[1:])
	case "daemon":
		return daemonCommand(ctx, args[1:])
	case "project":
		return project(ctx, args[1:])
	case "status":
		return statusCommand(ctx, args[1:])
	case "wait":
		return waitCommand(ctx, args[1:])
	case "home":
		return homeCommand(ctx, args[1:])
	case "knowledge":
		return knowledgeCommand(ctx, args[1:])
	case "task":
		return task(ctx, args[1:])
	case "worker":
		return workerCommand(ctx, args[1:])
	case "signal":
		return signalCommand(ctx, args[1:])
	case "commander":
		return commanderCommand(ctx, args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// exitError lets commands return a documented non-success status without
// treating it as an unexpected CLI failure.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func exitCode(err error) int {
	var status *exitError
	if errors.As(err, &status) {
		return status.code
	}
	return 1
}

func initialize(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	path := flags.String("db", "", "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := openStore(ctx, *path)
	if err != nil {
		return err
	}
	defer store.Close()
	fmt.Println(*path)
	return nil
}

func mission(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "list" {
		return missionList(ctx, args[1:])
	}
	if len(args) >= 1 && args[0] == "create" {
		return missionCreate(ctx, args[1:])
	}
	if len(args) >= 2 && args[0] == "timeline" {
		return timeline(ctx, "mission", args[1], args[2:])
	}
	if len(args) >= 2 && args[0] == "digest" {
		return missionDigest(ctx, domain.MissionID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "cancel" {
		return missionCancel(ctx, domain.MissionID(args[1]), args[2:])
	}
	return errors.New("expected: pintellect mission list|create|timeline|digest|cancel")
}

type missionListItem struct {
	ID          domain.MissionID    `json:"id"`
	Title       string              `json:"title"`
	State       domain.MissionState `json:"state"`
	TaskCounts  taskBucketCounts    `json:"task_counts"`
	OpenSignals int                 `json:"open_signals"`
	CreatedAt   time.Time           `json:"created_at"`
}

type taskBucketCounts struct {
	Active    int `json:"active"`
	Done      int `json:"done"`
	Attention int `json:"attention"`
}

func missionList(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mission list", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("mission list does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	missions, err := store.Missions(ctx)
	if err != nil {
		return err
	}
	items := make([]missionListItem, 0, len(missions))
	for _, item := range missions {
		tasks, err := store.Tasks(ctx, item.ID)
		if err != nil {
			return err
		}
		open, err := store.Signals(ctx, db.ListSignalsFilter{MissionID: item.ID, Status: "open"})
		if err != nil {
			return err
		}
		view := missionListItem{ID: item.ID, Title: item.Title, State: item.State, OpenSignals: len(open), CreatedAt: item.CreatedAt}
		for _, task := range tasks {
			addTaskBucket(&view.TaskCounts, task.State)
		}
		items = append(items, view)
	}
	if *jsonOutput {
		return encode(items)
	}
	fmt.Println("ID\tTITLE\tSTATE\tACTIVE\tDONE\tATTENTION\tOPEN SIGNALS\tCREATED")
	for _, item := range items {
		fmt.Printf("%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\n", item.ID, item.Title, item.State, item.TaskCounts.Active, item.TaskCounts.Done, item.TaskCounts.Attention, item.OpenSignals, item.CreatedAt.Format("2006-01-02"))
	}
	return nil
}

func addTaskBucket(counts *taskBucketCounts, state domain.TaskState) {
	switch state {
	case domain.TaskBlocked, domain.TaskNeedsAttention, domain.TaskFailed, domain.TaskDeliveryBlocked:
		counts.Attention++
	case domain.TaskReady, domain.TaskReportReady, domain.TaskDelivered, domain.TaskDeliveredBranch, domain.TaskCancelled:
		counts.Done++
	default:
		counts.Active++
	}
}

func missionCancel(ctx context.Context, missionID domain.MissionID, args []string) error {
	flags := flag.NewFlagSet("mission cancel", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	treehouseBinary := flags.String("treehouse", "treehouse", "Treehouse CLI binary")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "default", "explicit Herdr session name")
	herdrWorkspace := flags.String("herdr-workspace-label", "pintellect", "Herdr workspace presentation label")
	commandValue := flags.String("command-id", "", "idempotency command ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("mission cancel does not accept positional arguments")
	}
	if strings.TrimSpace(*herdrSession) == "" {
		return errors.New("mission cancel requires an explicit --herdr-session")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	command, err := suppliedCommandID(*commandValue)
	if err != nil {
		return err
	}
	canceller := worker.Canceller{Store: store, Treehouse: treehouse.NewService(store, treehouse.NewCommandClient(*treehouseBinary), gitcontrol.NewClient()), Herdr: herdr.NewCommandAdapter(*herdrBinary, *herdrSession, *herdrWorkspace)}
	cancelled, err := (&worker.MissionCanceller{Store: store, Tasks: &canceller}).Cancel(ctx, missionID, command)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(cancelled)
	}
	fmt.Printf("Mission %s cancelled\n", cancelled.ID)
	return nil
}

func missionDigest(ctx context.Context, missionID domain.MissionID, args []string) error {
	flags := flag.NewFlagSet("mission digest", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit artifact metadata and content as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("mission digest does not accept positional arguments")
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
	artifact, err := store.RegenerateMissionDigest(ctx, command, db.RegenerateMissionDigestInput{
		MissionID: missionID, Actor: "operator", Reason: "explicit command",
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(artifact)
	}
	_, err = os.Stdout.Write([]byte(artifact.Content))
	return err
}

func statusCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	missionID := flags.String("mission", "", "mission ID")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	var snapshot statusview.Snapshot
	if strings.TrimSpace(*missionID) == "" {
		missions, err := store.Missions(ctx)
		if err != nil {
			return err
		}
		switch len(missions) {
		case 0:
			snapshot = statusview.Empty()
		case 1:
			snapshot, err = statusview.Load(ctx, store, missions[0].ID)
		default:
			return errors.New("status requires --mission ID when multiple missions exist")
		}
	} else {
		snapshot, err = statusview.Load(ctx, store, domain.MissionID(*missionID))
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(snapshot)
	}
	printStatus(snapshot)
	return nil
}

func printStatus(snapshot statusview.Snapshot) {
	if snapshot.Mission != nil {
		fmt.Printf("Mission: %s (%s)\n", snapshot.Mission.Title, snapshot.Mission.ID)
	}
	fmt.Println("Needs Your Attention")
	for _, task := range snapshot.NeedsYourAttention.Tasks {
		fmt.Printf("  task  %s [%s] %s\n", task.ID, task.State, task.Title)
	}
	for _, signal := range snapshot.NeedsYourAttention.Signals {
		fmt.Printf("  signal %s [%s] %s\n", signal.ID, signal.Kind, signal.Question)
	}
	fmt.Println("Recently Completed")
	for _, task := range snapshot.RecentlyCompleted {
		fmt.Printf("  %s [%s] %s\n", task.ID, task.State, task.Title)
	}
	fmt.Println("Underway")
	for _, task := range snapshot.Underway {
		fmt.Printf("  %s [%s] %s\n", task.ID, task.State, task.Title)
	}
	fmt.Println("Up Next")
	for _, task := range snapshot.UpNext {
		fmt.Printf("  %s [%s] %s\n", task.ID, task.State, task.Title)
	}
}

func missionCreate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mission create", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	projectPath := flags.String("project", "", "registered project path")
	projectName := flags.String("project-name", "", "registered project name")
	title := flags.String("title", "", "mission title")
	objective := flags.String("objective", "", "mission objective")
	operatorMessage := flags.String("operator-message", "", "verbatim operator intake direction")
	maxAttempts := flags.Int("max-task-attempts", 0, "maximum attempts per task (0 is unlimited)")
	maxDuration := flags.Duration("max-duration", 0, "maximum mission wall-clock duration (0 is unlimited)")
	maxConcurrent := flags.Int("max-concurrent-tasks", 0, "maximum concurrently active tasks (0 is unlimited)")
	maxValidation := flags.Int("max-validation-rounds", 0, "maximum validation rounds per task (0 is unlimited)")
	var criteria stringList
	flags.Var(&criteria, "acceptance", "acceptance criterion (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectPath == "" {
		return errors.New("mission create requires --project")
	}
	absoluteProject, err := filepath.Abs(*projectPath)
	if err != nil {
		return fmt.Errorf("resolve project path: %w", err)
	}
	name := strings.TrimSpace(*projectName)
	if name == "" {
		name = filepath.Base(absoluteProject)
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	projectID, err := store.ProjectByPath(ctx, absoluteProject)
	if errors.Is(err, db.ErrNotFound) {
		projectCommand, commandErr := commandID()
		if commandErr != nil {
			return commandErr
		}
		projectID, err = store.CreateProject(ctx, projectCommand, db.CreateProjectInput{Name: name, Path: absoluteProject})
	}
	if err != nil {
		return err
	}
	missionCommand, err := commandID()
	if err != nil {
		return err
	}
	created, err := store.CreateMission(ctx, missionCommand, db.CreateMissionInput{
		ProjectID: projectID, Title: *title, Objective: *objective, OperatorMessage: *operatorMessage, AcceptanceCriteria: criteriaValues(criteria),
		Budget: domain.MissionBudget{MaxWallClock: *maxDuration, MaxConcurrentTasks: *maxConcurrent,
			MaxTaskAttempts: *maxAttempts, MaxValidationRuns: *maxValidation},
	})
	if err != nil {
		return err
	}
	return encode(created)
}

func task(ctx context.Context, args []string) error {
	if len(args) >= 2 && args[0] == "create" {
		return taskCreate(ctx, domain.MissionID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "start" {
		return taskStart(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "retry" {
		return taskRetry(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "cancel" {
		return taskCancel(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "validate" {
		return taskValidate(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "deliver" {
		return taskDeliver(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "release" {
		return taskRelease(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "timeline" {
		return timeline(ctx, "task", args[1], args[2:])
	}
	return errors.New("expected: pintellect task create MISSION|start TASK|retry TASK|cancel TASK|validate TASK|deliver TASK|release TASK|timeline TASK")
}

func taskDeliver(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("task deliver", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	gitBinary := flags.String("git", "git", "Git binary")
	ghBinary := flags.String("gh-axi", "gh-axi", "gh-axi binary")
	gateBinary := flags.String("no-mistakes", "no-mistakes", "no-mistakes CLI binary")
	base := flags.String("base", "", "pull request base branch")
	commandValue := flags.String("command-id", "", "idempotency command ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	command, err := suppliedCommandID(*commandValue)
	if err != nil {
		return err
	}
	service := delivery.Service{
		Store:  store,
		Git:    delivery.CommandGit{Binary: *gitBinary},
		Remote: delivery.CommandRemote{GitBinary: *gitBinary, GHBinary: *ghBinary},
		Gate:   delivery.CommandGate{Binary: *gateBinary},
	}
	result, err := service.Deliver(ctx, delivery.Request{
		TaskID: taskID, CommandID: command, Base: *base, Actor: "operator",
	})
	if errors.Is(err, delivery.ErrGateFailed) {
		if encodeErr := encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	if err != nil {
		return err
	}
	return encode(result)
}

func taskRelease(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("task release", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	treehouseBinary := flags.String("treehouse", "treehouse", "Treehouse CLI binary")
	gitBinary := flags.String("git", "git", "Git binary")
	commandValue := flags.String("command-id", "", "idempotency command ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	command, err := suppliedCommandID(*commandValue)
	if err != nil {
		return err
	}
	leaseService := treehouse.NewService(store, treehouse.NewCommandClient(*treehouseBinary), &gitcontrol.Client{Binary: *gitBinary})
	service := delivery.Service{Store: store, Leases: leaseService}
	released, err := service.Release(ctx, taskID, command, "operator")
	if err != nil {
		return err
	}
	return encode(released)
}

func taskRetry(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("task retry", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	current, err := store.Task(ctx, taskID)
	if err != nil {
		return err
	}
	command, err := commandID()
	if err != nil {
		return err
	}
	retried, err := store.RetryTask(ctx, command, db.RetryTaskInput{TaskID: taskID, ExpectedVersion: current.Version, Actor: "operator"})
	if err != nil {
		return err
	}
	return encode(retried)
}

func taskCancel(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("task cancel", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	treehouseBinary := flags.String("treehouse", "treehouse", "Treehouse CLI binary")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "default", "explicit Herdr session name")
	herdrWorkspace := flags.String("herdr-workspace-label", "pintellect", "Herdr workspace presentation label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*herdrSession) == "" {
		return errors.New("task cancel requires an explicit --herdr-session")
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
	canceller := worker.Canceller{Store: store, Treehouse: treehouse.NewService(store, treehouse.NewCommandClient(*treehouseBinary), gitcontrol.NewClient()), Herdr: herdr.NewCommandAdapter(*herdrBinary, *herdrSession, *herdrWorkspace)}
	cancelled, err := canceller.Cancel(ctx, taskID, command)
	if err != nil {
		return err
	}
	return encode(cancelled)
}

func taskCreate(ctx context.Context, missionID domain.MissionID, args []string) error {
	flags := flag.NewFlagSet("task create", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	title := flags.String("title", "", "task title")
	objective := flags.String("objective", "", "task objective")
	delivery := flags.String("delivery", string(domain.DeliveryBranch), "delivery mode")
	workerAgent := flags.String("agent", "codex", "worker runtime")
	priority := flags.Int("priority", 0, "task priority")
	var criteria stringList
	flags.Var(&criteria, "acceptance", "acceptance criterion (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *workerAgent != "codex" {
		return errors.New("milestone 3 task create supports only --agent codex")
	}
	if len(criteria) == 0 {
		return errors.New("task create requires at least one --acceptance criterion")
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
	created, err := store.CreateTask(ctx, command, db.CreateTaskInput{
		MissionID: missionID, Kind: domain.TaskImplementation, Title: *title, Objective: *objective,
		AcceptanceCriteria: criteriaValues(criteria), Priority: *priority, WorkerAgent: *workerAgent,
		DeliveryMode: domain.DeliveryMode(*delivery),
	})
	if err != nil {
		return err
	}
	return encode(created)
}

func taskStart(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("task start", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	treehouseBinary := flags.String("treehouse", "treehouse", "Treehouse CLI binary")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "default", "explicit Herdr session name")
	herdrWorkspace := flags.String("herdr-workspace-label", "pintellect", "Herdr workspace presentation label")
	taskFiles := flags.String("task-files", "", "task artifact base directory")
	maxWorkerRuntime := flags.Duration("max-worker-runtime", 0, "maximum worker runtime (0 is unlimited)")
	maxWorkerRestarts := flags.Int("max-worker-restarts", 0, "maximum worker restarts (0 is unlimited)")
	maxFixRounds := flags.Int("max-fix-rounds", 0, "maximum worker fix rounds (0 is unlimited)")
	var validation stringList
	flags.Var(&validation, "validate", "required validation command or instruction (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*herdrSession) == "" {
		return errors.New("task start requires an explicit --herdr-session")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	treehouseService := treehouse.NewService(store, treehouse.NewCommandClient(*treehouseBinary), gitcontrol.NewClient())
	starter := worker.Starter{
		Store: store, Treehouse: treehouseService,
		Herdr:  herdr.NewCommandAdapter(*herdrBinary, *herdrSession, *herdrWorkspace),
		Briefs: worker.BriefGenerator{BaseDir: *taskFiles}, Validation: validation,
		Budget: domain.WorkerBudget{MaxRuntime: *maxWorkerRuntime, MaxRestarts: *maxWorkerRestarts,
			MaxFixRounds: *maxFixRounds},
	}
	started, err := starter.Start(ctx, taskID)
	if err != nil {
		return err
	}
	return encode(started)
}

func taskValidate(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("task validate", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	gitBinary := flags.String("git", "git", "Git binary")
	validatorVersion := flags.String("validator-version", "command-v1", "version of the command validator")
	var unitTests, typechecks, lints, projectValidations stringList
	flags.Var(&unitTests, "unit-test", "unit-test command (repeatable)")
	flags.Var(&typechecks, "typecheck", "typecheck command (repeatable)")
	flags.Var(&lints, "lint", "lint command (repeatable)")
	flags.Var(&projectValidations, "project-validation", "project validation command (repeatable)")
	flags.Var(&projectValidations, "validate", "project validation command (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	type validatorConfig struct {
		Kind     validationcore.Kind `json:"kind"`
		Commands []string            `json:"commands"`
	}
	configured := []validatorConfig{
		{validationcore.UnitTests, unitTests},
		{validationcore.Typecheck, typechecks},
		{validationcore.Lint, lints},
		{validationcore.ProjectValidation, projectValidations},
	}
	validators := make([]validationcore.Validator, 0,
		len(unitTests)+len(typechecks)+len(lints)+len(projectValidations))
	for _, group := range configured {
		for _, command := range group.Commands {
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("%s validator command cannot be empty", group.Kind)
			}
			validators = append(validators, validationcore.ShellValidator(group.Kind, *validatorVersion, command))
		}
	}
	if len(validators) == 0 {
		return errors.New("task validate requires at least one validation command")
	}
	config, err := json.Marshal(configured)
	if err != nil {
		return fmt.Errorf("encode validation config: %w", err)
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
	pipeline := validationcore.Pipeline{
		Store:       store,
		Workspace:   validationcore.GitFingerprinter{Binary: *gitBinary},
		Environment: validationcore.ProcessEnvironment{},
	}
	report, err := pipeline.ValidateTask(ctx, validationcore.Request{
		TaskID: taskID, CommandID: command, Validators: validators, Config: config, Actor: "commander",
	})
	if err != nil {
		return err
	}
	if err := encode(report); err != nil {
		return err
	}
	if !report.Passed {
		return errors.New("validation failed")
	}
	return nil
}

func workerCommand(ctx context.Context, args []string) error {
	if len(args) >= 2 && args[0] == "inspect" {
		return workerInspect(ctx, domain.TaskID(args[1]), args[2:])
	}
	if len(args) >= 2 && args[0] == "complete" {
		return workerComplete(ctx, domain.TaskID(args[1]), args[2:])
	}
	return errors.New("expected: pintellect worker inspect TASK [--attempt N] [--db PATH] [--json]|complete TASK --attempt N --head-sha SHA --result FILE")
}

func workerInspect(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("worker inspect", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	attempt := flags.Int("attempt", 0, "task attempt (defaults to current)")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("worker inspect does not accept positional arguments")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if *attempt == 0 {
		task, err := store.Task(ctx, taskID)
		if err != nil {
			return err
		}
		*attempt = task.CurrentAttempt
	}
	session, err := store.WorkerSession(ctx, taskID, *attempt)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(session)
	}
	fmt.Printf("%s\t%s\t%s\t%s\n", session.ID, session.State, session.HerdrPaneID, session.AgentSessionID)
	return nil
}

func workerComplete(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("worker complete", flag.ContinueOnError)
	dbPath := flags.String("db", "", "SQLite database path")
	treehouseBinary := flags.String("treehouse", "treehouse", "Treehouse CLI binary")
	taskFiles := flags.String("task-files", "", "task artifact base directory")
	attempt := flags.Int("attempt", 0, "task attempt")
	headSHA := flags.String("head-sha", "", "immutable completed head SHA")
	resultPath := flags.String("result", "", "structured result JSON path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*resultPath) == "" {
		return errors.New("worker complete requires --result")
	}
	store, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	absoluteResult, err := filepath.Abs(*resultPath)
	if err != nil {
		return err
	}
	command := worker.CompletionCommandID(taskID, *attempt, *headSHA, absoluteResult)
	completer := worker.Completer{Store: store, Git: gitcontrol.NewClient(),
		Leases: treehouse.NewCommandClient(*treehouseBinary), TaskFiles: worker.BriefGenerator{BaseDir: *taskFiles}}
	completed, err := completer.Complete(ctx, worker.CompleteRequest{
		TaskID: taskID, Attempt: *attempt, HeadSHA: *headSHA, ResultPath: absoluteResult, CommandID: command,
	})
	if err != nil {
		return err
	}
	return encode(completed)
}

func timeline(ctx context.Context, kind, rawID string, args []string) error {
	flags := flag.NewFlagSet("timeline", flag.ContinueOnError)
	path := flags.String("db", "", "SQLite database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := db.Open(ctx, *path)
	if err != nil {
		return err
	}
	defer store.Close()
	var events []domain.Event
	if kind == "task" {
		events, err = store.TaskEvents(ctx, domain.TaskID(rawID))
	} else {
		events, err = store.MissionEvents(ctx, domain.MissionID(rawID))
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(events)
	}
	for _, event := range events {
		fmt.Printf("%d\t%s\t%s\t%s\n", event.Sequence, event.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"), event.Actor, event.Type)
	}
	return nil
}

func openStore(ctx context.Context, path string) (*db.Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	return db.Open(ctx, path)
}

func commandID() (domain.CommandID, error) {
	raw, err := id.New("cmd")
	return domain.CommandID(raw), err
}

func suppliedCommandID(value string) (domain.CommandID, error) {
	if strings.TrimSpace(value) != "" {
		return domain.CommandID(value), nil
	}
	return commandID()
}

func encode(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ", ") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func criteriaValues(values []string) []domain.Criterion {
	criteria := make([]domain.Criterion, 0, len(values))
	for _, value := range values {
		criteria = append(criteria, domain.Criterion{Description: value})
	}
	return criteria
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  pintellect init [--db PATH]
  pintellect home [--agent codex|claude] [--db PATH]
  pintellect status --mission ID [--db PATH] [--json]
  pintellect wait --mission ID [--timeout DURATION] [--after-seq N] [--db PATH]
  pintellect project add PATH [--name NAME] [--db PATH] [--json]
  pintellect project list [--db PATH] [--json]
  pintellect project inspect NAME [--db PATH] [--json]
  pintellect mission create --project PATH --title TITLE --objective OBJECTIVE [--acceptance TEXT]
  pintellect mission list [--db PATH] [--json]
  pintellect mission cancel ID [--db PATH] [--json]
  pintellect task create MISSION --title TITLE --objective OBJECTIVE [--acceptance TEXT]
  pintellect task start TASK [--herdr-session NAME] [--db PATH]
  pintellect task retry TASK [--db PATH]
  pintellect task cancel TASK [--herdr-session NAME] [--db PATH]
  pintellect task validate TASK --unit-test COMMAND [--typecheck COMMAND] [--lint COMMAND] [--project-validation COMMAND]
  pintellect task deliver TASK [--command-id ID] [--base BRANCH] [--db PATH]
  pintellect task release TASK [--command-id ID] [--db PATH]
  pintellect worker complete TASK --attempt N --head-sha SHA --result FILE [--db PATH]
  pintellect worker inspect TASK [--attempt N] [--db PATH] [--json]
  pintellect task|mission timeline ID [--db PATH] [--json]
  pintellect signal raise --mission ID --question TEXT [--task ID] [--kind KIND] [--context TEXT] [--recommendation TEXT] [--command-id ID] [--db PATH] [--json]
  pintellect signal list [--mission ID] [--status STATUS] [--db PATH] [--json]
  pintellect signal inspect <id> [--db PATH] [--json]
  pintellect signal resolve <id> --answer ANSWER [--command-id ID] [--db PATH] [--json]
  pintellect commander start --agent pi|claude|codex --mission ID [--herdr-session NAME] [--max-turns N] [--max-duration DURATION]
  pintellect commander renew [--session ID] [--mission ID] [--max-turns N] [--max-duration DURATION] [--command-id ID]
  pintellect commander prompt|steer|follow-up MESSAGE [--mission ID]
  pintellect commander attach|status [--mission ID]
  pintellect daemon status|start|stop|restart [--db PATH]
  pintellect knowledge list [--status candidate|active] [--db PATH] [--json]
  pintellect knowledge promote|reject ID [--db PATH]
  pintellect knowledge supersede ID --by REPLACEMENT [--db PATH]
  pintellect version`)
}
