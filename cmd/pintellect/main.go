package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"parallel-intellect/internal/db"
	"parallel-intellect/internal/domain"
	gitcontrol "parallel-intellect/internal/git"
	"parallel-intellect/internal/herdr"
	"parallel-intellect/internal/id"
	"parallel-intellect/internal/treehouse"
	"parallel-intellect/internal/worker"
)

const version = "0.3.0-m3"

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
		return initialize(ctx, args[1:])
	case "mission":
		return mission(ctx, args[1:])
	case "task":
		return task(ctx, args[1:])
	case "worker":
		return workerCommand(ctx, args[1:])
	case "signal":
		return signalCommand(ctx, args[1:])
	case "commander":
		return commander(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func initialize(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	path := flags.String("db", "parallel-intellect.db", "SQLite database path")
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
	if len(args) >= 1 && args[0] == "create" {
		return missionCreate(ctx, args[1:])
	}
	if len(args) >= 2 && args[0] == "timeline" {
		return timeline(ctx, "mission", args[1], args[2:])
	}
	return errors.New("expected: pintellect mission create|timeline")
}

func missionCreate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mission create", flag.ContinueOnError)
	dbPath := flags.String("db", "parallel-intellect.db", "SQLite database path")
	projectPath := flags.String("project", "", "registered project path")
	projectName := flags.String("project-name", "", "registered project name")
	title := flags.String("title", "", "mission title")
	objective := flags.String("objective", "", "mission objective")
	maxAttempts := flags.Int("max-task-attempts", 3, "maximum attempts per task")
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
		ProjectID: projectID, Title: *title, Objective: *objective, AcceptanceCriteria: criteriaValues(criteria),
		Budget: domain.MissionBudget{MaxTaskAttempts: *maxAttempts},
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
	if len(args) >= 2 && args[0] == "timeline" {
		return timeline(ctx, "task", args[1], args[2:])
	}
	return errors.New("expected: pintellect task create MISSION|start TASK|timeline TASK")
}

func taskCreate(ctx context.Context, missionID domain.MissionID, args []string) error {
	flags := flag.NewFlagSet("task create", flag.ContinueOnError)
	dbPath := flags.String("db", "parallel-intellect.db", "SQLite database path")
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
	dbPath := flags.String("db", "parallel-intellect.db", "SQLite database path")
	treehouseBinary := flags.String("treehouse", "treehouse", "Treehouse CLI binary")
	herdrBinary := flags.String("herdr", "herdr", "Herdr CLI binary")
	herdrSession := flags.String("herdr-session", "default", "explicit Herdr session name")
	herdrWorkspace := flags.String("herdr-workspace-label", "Parallel Intellect", "Herdr workspace presentation label")
	taskFiles := flags.String("task-files", "", "task artifact base directory")
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
	}
	started, err := starter.Start(ctx, taskID)
	if err != nil {
		return err
	}
	return encode(started)
}

func workerCommand(ctx context.Context, args []string) error {
	if len(args) >= 2 && args[0] == "complete" {
		return workerComplete(ctx, domain.TaskID(args[1]), args[2:])
	}
	return errors.New("expected: pintellect worker complete TASK --attempt N --head-sha SHA --result FILE")
}

func workerComplete(ctx context.Context, taskID domain.TaskID, args []string) error {
	flags := flag.NewFlagSet("worker complete", flag.ContinueOnError)
	dbPath := flags.String("db", "parallel-intellect.db", "SQLite database path")
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
	command := completionCommandID(taskID, *attempt, *headSHA, absoluteResult)
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

func commander(args []string) error {
	if len(args) == 0 {
		return errors.New("expected: pintellect commander start|attach|status")
	}
	return fmt.Errorf("commander %s is reserved for a later runtime milestone", args[0])
}

func timeline(ctx context.Context, kind, rawID string, args []string) error {
	flags := flag.NewFlagSet("timeline", flag.ContinueOnError)
	path := flags.String("db", "parallel-intellect.db", "SQLite database path")
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

func completionCommandID(taskID domain.TaskID, attempt int, headSHA, resultPath string) domain.CommandID {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s", taskID, attempt, strings.ToLower(headSHA), resultPath)))
	return domain.CommandID(fmt.Sprintf("cmd_worker_complete_%x", digest[:16]))
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
  pintellect mission create --project PATH --title TITLE --objective OBJECTIVE [--acceptance TEXT]
  pintellect task create MISSION --title TITLE --objective OBJECTIVE [--acceptance TEXT]
  pintellect task start TASK [--herdr-session NAME] [--db PATH]
  pintellect worker complete TASK --attempt N --head-sha SHA --result FILE [--db PATH]
  pintellect task|mission timeline ID [--db PATH] [--json]
  pintellect signal list [--mission ID] [--status STATUS] [--db PATH] [--json]
  pintellect signal inspect <id> [--db PATH] [--json]
  pintellect signal resolve <id> --answer ANSWER [--command-id ID] [--db PATH] [--json]
  pintellect commander start|attach|status
  pintellect version`)
}
