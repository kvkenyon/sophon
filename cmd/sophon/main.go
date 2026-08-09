// Command sophon is the operator and commander entrypoint to the filesystem
// protocol: every command is one short-lived invocation over internal/flow.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"sophon/internal/datahome"
	"sophon/internal/domain"
	"sophon/internal/flow"
	"sophon/internal/herdr"
	"sophon/internal/id"
	"sophon/internal/monitor"
	"sophon/internal/readcode"
	"sophon/internal/reviewbridge"
	"sophon/internal/store"
	"sophon/internal/workspace"
	runtimeprompts "sophon/prompts"
)

const version = "0.6.0-m1"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sophon:", err)
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
	case "mission":
		return missionCommand(ctx, args[1:])
	case "workspace":
		return workspaceCommand(ctx, args[1:])
	case "project":
		return projectCommand(ctx, args[1:])
	case "task":
		return taskCommand(ctx, args[1:])
	case "spawn":
		return spawnCommand(ctx, args[1:])
	case "revise":
		return reviseCommand(ctx, args[1:])
	case "worker":
		return workerCommand(ctx, args[1:])
	case "commander":
		return commanderCommand(ctx, args[1:])
	case "monitor":
		return monitorCommand(ctx, args[1:])
	case "review":
		return reviewCommand(ctx, args[1:])
	case "verify-complete":
		return verifyCompleteCommand(ctx, args[1:])
	case "validate":
		return validateCommand(ctx, args[1:])
	case "deliver":
		return deliverCommand(ctx, args[1:])
	case "delivery":
		return deliverySelectionCommand(ctx, args[1:])
	case "release":
		return releaseCommand(ctx, args[1:])
	case "status":
		return statusCommand(ctx, args[1:])
	case "send":
		return sendCommand(ctx, args[1:])
	case "prompt":
		return promptCommand(ctx, args[1:])
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

// toolConfig carries the external binary/session flags. Only commands that
// touch an external boundary bind the flags they need.
type toolConfig struct {
	herdr        string
	treehouse    string
	git          string
	ghAxi        string
	readCode     string
	herdrSession string
}

func defaultTools() toolConfig {
	session := strings.TrimSpace(os.Getenv("HERDR_SESSION"))
	if session == "" {
		session = "default"
	}
	return toolConfig{herdr: "herdr", treehouse: "treehouse", git: "git", ghAxi: "gh-axi",
		readCode: strings.TrimSpace(os.Getenv("SOPHON_READ_THE_CODE")), herdrSession: session}
}

func (t *toolConfig) bind(flags *flag.FlagSet, names ...string) {
	for _, name := range names {
		switch name {
		case "herdr":
			flags.StringVar(&t.herdr, "herdr", t.herdr, "Herdr CLI binary")
		case "treehouse":
			flags.StringVar(&t.treehouse, "treehouse", t.treehouse, "Treehouse CLI binary")
		case "git":
			flags.StringVar(&t.git, "git", t.git, "Git binary")
		case "gh-axi":
			flags.StringVar(&t.ghAxi, "gh-axi", t.ghAxi, "gh-axi binary")
		case "read-the-code":
			flags.StringVar(&t.readCode, "read-the-code", t.readCode, "configured read-the-code-axi executable (env SOPHON_READ_THE_CODE)")
		case "herdr-session":
			flags.StringVar(&t.herdrSession, "herdr-session", t.herdrSession, "Herdr session name (env HERDR_SESSION)")
		}
	}
}

func (t toolConfig) flow() *flow.Flow {
	panes := herdr.NewCommandAdapter(t.herdr, t.herdrSession, "sophon")
	deps := flow.ProductionDeps(t.git, t.treehouse, t.ghAxi, panes)
	deps.ReviewProduct = readcode.Client{Binary: t.readCode}
	deps.HerdrSession = t.herdrSession
	binary := t.herdr
	deps.NewSessionPanes = func(session string) flow.SessionPanes {
		return herdr.NewCommandAdapter(binary, session, "sophon")
	}
	return flow.New(deps)
}

// parseFlags parses interspersed flags and positionals: the standard flag
// package stops at the first positional, but the documented command forms
// place flags after the task ID. Positionals are returned in order.
func parseFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for len(rest) > 0 {
		if err := flags.Parse(rest); err != nil {
			return nil, err
		}
		rest = flags.Args()
		if len(rest) > 0 {
			positional = append(positional, rest[0])
			rest = rest[1:]
		}
	}
	return positional, nil
}

func workspaceCommand(_ context.Context, args []string) error {
	if len(args) < 1 {
		return &exitError{2, errors.New("expected: sophon workspace init|inspect")}
	}
	flags := flag.NewFlagSet("workspace "+args[0], flag.ContinueOnError)
	positional, err := parseFlags(flags, args[1:])
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("workspace command requires exactly one root path")
	}
	switch args[0] {
	case "init":
		marker, err := workspace.Init(positional[0])
		if err != nil {
			return err
		}
		return encode(marker)
	case "inspect":
		marker, err := workspace.Read(positional[0])
		if err != nil {
			return err
		}
		return encode(marker)
	default:
		return &exitError{2, errors.New("expected: sophon workspace init|inspect")}
	}
}

func projectCommand(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return &exitError{2, errors.New("expected: sophon project list|create|clone|add|inspect|publish")}
	}
	operation := args[0]
	flags := flag.NewFlagSet("project "+operation, flag.ContinueOnError)
	root := flags.String("workspace", "", "Sophon workspace root")
	gitBinary := flags.String("git", "git", "Git binary")
	branch := flags.String("initial-branch", "main", "unborn local default branch")
	source := flags.String("source", "", "explicit Git clone source")
	path := flags.String("path", "", "existing path already at projects/<key>")
	repository := flags.String("repository", "", "exact GitHub owner/repository to create")
	remoteURL := flags.String("remote-url", "", "exact origin URL to add after creation")
	visibility := flags.String("visibility", "private", "GitHub visibility (private|public|internal)")
	confirmed := flags.Bool("confirmed", false, "confirm this exact GitHub resource and remote creation")
	ghBinary := flags.String("gh-axi", "gh-axi", "gh-axi binary")
	positional, err := parseFlags(flags, args[1:])
	if err != nil {
		return err
	}
	inspector := workspace.Inspector{GitBinary: *gitBinary}
	switch operation {
	case "list":
		if len(positional) != 0 {
			return errors.New("project list does not accept a project key")
		}
		projects, err := inspector.List(ctx, *root)
		if err != nil {
			return err
		}
		if projects == nil {
			projects = []workspace.Project{}
		}
		return encode(projects)
	case "create", "clone", "add", "inspect", "publish":
		if len(positional) != 1 {
			return fmt.Errorf("project %s requires exactly one project key", operation)
		}
	default:
		return &exitError{2, errors.New("expected: sophon project list|create|clone|add|inspect|publish")}
	}
	key := positional[0]
	var project workspace.Project
	switch operation {
	case "create":
		project, err = inspector.Create(ctx, *root, key, *branch)
	case "clone":
		project, err = inspector.Clone(ctx, *root, key, *source)
	case "add":
		if strings.TrimSpace(*path) != "" {
			expected, pathErr := filepath.Abs(filepath.Join(*root, workspace.ProjectsDir, key))
			observed, observedErr := filepath.Abs(*path)
			if pathErr != nil || observedErr != nil || filepath.Clean(expected) != filepath.Clean(observed) {
				return errors.New("project add only adopts an existing real child already at workspace/projects/<key>; clone external repositories instead")
			}
		}
		project, err = inspector.Add(ctx, *root, key)
	case "inspect":
		project, err = inspector.Resolve(ctx, *root, key)
	case "publish":
		publication, publishErr := inspector.PublishGitHub(ctx, *root, key, *repository, *remoteURL, *visibility, *ghBinary, *confirmed)
		if publishErr != nil {
			return publishErr
		}
		return encode(publication)
	}
	if err != nil {
		return err
	}
	return encode(project)
}

func missionCommand(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "create" {
		return missionCreate(ctx, args[1:])
	}
	if len(args) >= 1 && args[0] == "list" {
		return missionList(ctx, args[1:])
	}
	return &exitError{2, errors.New("expected: sophon mission create|list")}
}

func missionCreate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mission create", flag.ContinueOnError)
	workspaceRoot := flags.String("workspace", "", "Sophon workspace root")
	project := flags.String("project", "", "workspace project key, or legacy repository path without --workspace")
	title := flags.String("title", "", "mission title")
	objective := flags.String("objective", "", "mission objective")
	gitBinary := flags.String("git", "git", "Git binary")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("mission create does not accept positional arguments")
	}
	var created store.Mission
	if strings.TrimSpace(*workspaceRoot) != "" {
		created, err = flow.New(flow.Deps{Projects: workspace.Inspector{GitBinary: *gitBinary}}).
			CreateWorkspaceMission(ctx, *workspaceRoot, *project, *title, *objective)
	} else {
		created, err = flow.New(flow.Deps{}).CreateMission(ctx, *project, *title, *objective)
	}
	if err != nil {
		return err
	}
	return encode(created)
}

func missionList(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("mission list", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("mission list does not accept positional arguments")
	}
	missions, err := store.ListMissions()
	if err != nil {
		return err
	}
	if *jsonOutput {
		if missions == nil {
			missions = []store.Mission{}
		}
		return encode(missions)
	}
	fmt.Println("ID\tTITLE\tPROJECT\tCREATED")
	for _, mission := range missions {
		project := mission.ProjectKey
		if project == "" {
			project = filepath.Base(mission.ProjectPath)
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", mission.ID, mission.Title, project, mission.CreatedAt.Format("2006-01-02"))
	}
	return nil
}

func taskCommand(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "create" {
		return taskCreate(ctx, args[1:])
	}
	if len(args) >= 1 && args[0] == "cancel" {
		return taskCancel(ctx, args[1:])
	}
	if len(args) >= 1 && args[0] == "revise" {
		return taskRevise(ctx, args[1:])
	}
	return &exitError{2, errors.New("expected: sophon task create|cancel|revise")}
}

func taskCreate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("task create", flag.ContinueOnError)
	missionID := flags.String("mission", "", "mission ID")
	title := flags.String("title", "", "concise public task and pull request title")
	objective := flags.String("objective", "", "detailed worker objective")
	deliveryBranch := flags.String("delivery-branch", "", "explicit public-safe branch to push")
	kind := flags.String("kind", string(domain.TaskImplementation), "task kind")
	delivery := flags.String("delivery", "", "development/delivery posture (default local, or branch when --delivery-branch is supplied; local|branch|pr)")
	validate := flags.String("validate", "", "required validation command")
	review := flags.String("review", string(domain.ReviewOff), "Read the Code posture (off|optional|required)")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("task create does not accept positional arguments")
	}
	created, err := flow.New(flow.Deps{}).CreateTask(ctx, *missionID, *title, *objective, *deliveryBranch,
		domain.TaskKind(*kind), domain.DeliveryMode(*delivery), *validate, domain.ReviewPosture(*review))
	if err != nil {
		return err
	}
	return encode(created)
}

func taskCancel(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("task cancel", flag.ContinueOnError)
	reason := flags.String("reason", "", "explicit cancellation reason")
	confirmed := flags.Bool("confirmed", false, "confirm cancellation of this exact unstarted task")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("task cancel requires exactly one task ID")
	}
	value, err := flow.New(flow.Deps{}).CancelPlanned(ctx, positional[0], *reason, *confirmed)
	if err != nil {
		return err
	}
	return encode(value)
}

func taskRevise(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("task revise", flag.ContinueOnError)
	title := flags.String("title", "", "replacement task title")
	objective := flags.String("objective", "", "replacement detailed objective")
	validate := flags.String("validate", "", "replacement validation command")
	confirmed := flags.Bool("confirmed", false, "confirm replacement of this exact unstarted task")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("task revise requires exactly one task ID")
	}
	value, err := flow.New(flow.Deps{}).RevisePlanned(ctx, positional[0], *title, *objective, *validate, *confirmed)
	if err != nil {
		return err
	}
	return encode(value)
}

func spawnCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("spawn", flag.ContinueOnError)
	retry := flags.Bool("retry", false, "fence the current attempt and spawn the next")
	tools.bind(flags, "herdr", "treehouse", "git", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("spawn requires exactly one task ID")
	}
	spawned, err := tools.flow().Spawn(ctx, positional[0], *retry)
	if err != nil {
		return err
	}
	return encode(spawned)
}

func reviseCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("revise", flag.ContinueOnError)
	reason := flags.String("reason", "", "why the accepted feedback corrects the same product contract")
	objective := flags.String("objective", "", "bounded correction objective beyond the current PR head")
	acceptExternal := flags.Bool("accept-external-head", false, "explicitly accept a reviewed external fast-forward as the correction base")
	tools.bind(flags, "herdr", "treehouse", "git", "gh-axi", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("revise requires exactly one task ID")
	}
	spawned, err := tools.flow().Revise(ctx, positional[0], *reason, *objective, *acceptExternal)
	if err != nil {
		return err
	}
	return encode(spawned)
}

func workerCommand(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "complete" {
		return workerComplete(ctx, args[1:])
	}
	if len(args) >= 1 && args[0] == "report" {
		return workerReport(ctx, args[1:])
	}
	if len(args) >= 1 && args[0] == "progress" {
		return workerProgress(ctx, args[1:])
	}
	return &exitError{2, errors.New("expected: sophon worker complete|report|progress TASK [flags]")}
}

func workerComplete(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("worker complete", flag.ContinueOnError)
	attempt := flags.Int("attempt", 0, "task attempt")
	headSHA := flags.String("head-sha", "", "immutable completed head SHA")
	resultPath := flags.String("result", "", "structured result JSON path inside the attempt directory")
	tools.bind(flags, "git", "herdr")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("worker complete requires exactly one task ID")
	}
	digest, err := tools.flow().PublishResult(ctx, positional[0], *attempt, *headSHA, *resultPath)
	if err != nil {
		return err
	}
	notifyDurableChange(ctx, tools.flow(), positional[0], *attempt, monitor.ChangeCompletion)
	return encode(map[string]any{"task_id": positional[0], "attempt": *attempt, "result_sha256": digest})
}

func workerReport(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("worker report", flag.ContinueOnError)
	attempt := flags.Int("attempt", 0, "task attempt")
	headSHA := flags.String("head-sha", "", "live attempt head SHA")
	reportPath := flags.String("report", "", "structured report JSON staging path inside the attempt directory")
	tools.bind(flags, "git", "herdr")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("worker report requires exactly one task ID")
	}
	digest, err := tools.flow().PublishReport(ctx, positional[0], *attempt, *headSHA, *reportPath)
	if err != nil {
		return err
	}
	notifyDurableChange(ctx, tools.flow(), positional[0], *attempt, monitor.ChangeReport)
	return encode(map[string]any{"task_id": positional[0], "attempt": *attempt, "report_sha256": digest})
}

func workerProgress(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("worker progress", flag.ContinueOnError)
	attempt := flags.Int("attempt", 0, "task attempt")
	phase := flags.String("phase", "", "stable worker phase")
	message := flags.String("message", "", "optional bounded phase note")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || *attempt < 1 || !monitor.ValidPhase(*phase) {
		return errors.New("worker progress requires TASK --attempt N --phase investigating|implementing|testing|waiting|blocked")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	ack, deliveryErr := monitor.NewClient(home).Progress(positional[0], *attempt, *phase, *message)
	if deliveryErr != nil {
		fmt.Fprintf(os.Stderr, "sophon: progress notification unavailable (nonfatal): %v\n", deliveryErr)
	}
	return encode(ack)
}

func commanderCommand(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "attach" {
		return commanderAttach(ctx, args[1:])
	}
	return &exitError{2, errors.New("expected: sophon commander attach [--pane ID --workspace ID --tab ID]")}
}

// commanderAttach registers the volatile wake/placement address of the live
// commander. Ambient Herdr pane environment supplies the identity when the
// commander runs inside Herdr; flags override it explicitly.
func commanderAttach(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("commander attach", flag.ContinueOnError)
	pane := flags.String("pane", strings.TrimSpace(os.Getenv("HERDR_PANE_ID")), "Herdr pane ID (env HERDR_PANE_ID)")
	workspace := flags.String("workspace", strings.TrimSpace(os.Getenv("HERDR_WORKSPACE_ID")), "Herdr workspace ID (env HERDR_WORKSPACE_ID)")
	tab := flags.String("tab", strings.TrimSpace(os.Getenv("HERDR_TAB_ID")), "Herdr tab ID (env HERDR_TAB_ID)")
	scope := flags.String("scope", "", "Sophon workspace root visible to this commander")
	tools.bind(flags, "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("commander attach does not accept positional arguments")
	}
	registration, err := tools.flow().AttachCommander(ctx, flow.AttachRequest{
		Session: tools.herdrSession, WorkspaceID: *workspace, TabID: *tab, PaneID: *pane, ScopeRoot: *scope})
	if err != nil {
		return err
	}
	return encode(registration)
}

func reviewCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &exitError{2, errors.New("expected: sophon review set|open|status|feedback|classify|apply|acknowledge|reconcile|end")}
	}
	switch args[0] {
	case "set":
		return reviewSet(ctx, args[1:])
	case "open":
		return reviewOpen(ctx, args[1:])
	case "status":
		return reviewStatus(ctx, args[1:])
	case "feedback":
		return reviewFeedback(ctx, args[1:])
	case "classify":
		return reviewClassify(ctx, args[1:])
	case "apply":
		return reviewApply(ctx, args[1:])
	case "acknowledge":
		return reviewAcknowledge(ctx, args[1:])
	case "reconcile":
		return reviewReconcile(ctx, args[1:])
	case "end":
		return reviewEnd(ctx, args[1:])
	case "bridge":
		return reviewBridge(ctx, args[1:])
	default:
		return &exitError{2, fmt.Errorf("unknown review command %q", args[0])}
	}
}

func reviewSet(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("review set", flag.ContinueOnError)
	posture := flags.String("posture", "", "review posture escalation (optional|required)")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review set requires TASK --posture optional|required")
	}
	record, err := flow.New(flow.Deps{}).SetReviewPosture(ctx, positional[0], domain.ReviewPosture(*posture))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(record)
	}
	fmt.Printf("task %s review posture: %s -> %s\n", record.TaskID, record.From, record.To)
	return nil
}

func reviewOpen(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("review open", flag.ContinueOnError)
	attempt := flags.Int("attempt", 0, "exact current attempt (defaults to current)")
	noBrowser := flags.Bool("no-browser", false, "do not launch the browser")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON including the local capability URL")
	tools.bind(flags, "read-the-code", "git", "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || *attempt < 0 {
		return errors.New("review open requires one TASK and an optional positive --attempt")
	}
	result, err := tools.flow().ReviewOpen(ctx, positional[0], *attempt, *noBrowser)
	if err != nil {
		return err
	}
	if err := ensureReviewBridge(result.TaskID, result.Attempt, tools); err != nil {
		return fmt.Errorf("review opened but event bridge did not acquire its exact owner: %w", err)
	}
	if *jsonOutput {
		return encode(result)
	}
	fmt.Printf("%s review %s for task %s attempt %d (%s..%s)\n", map[bool]string{true: "Resumed", false: "Opened"}[result.Resumed],
		result.SessionID, result.TaskID, result.Attempt, result.BaseSHA[:10], result.HeadSHA[:10])
	if *noBrowser {
		// This is the only non-JSON surface that returns the bearer URL, directly
		// to the local operator who explicitly requested a no-browser open.
		fmt.Println(result.BrowserURL)
	}
	return nil
}

func reviewStatus(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("review status", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review status requires exactly one task ID")
	}
	status, err := flow.New(flow.Deps{}).ReviewStatus(positional[0])
	if err != nil {
		return err
	}
	if status.SessionID != "" {
		home, homeErr := datahome.AbsDir()
		task, taskErr := store.FindTask(status.TaskID)
		if homeErr == nil && taskErr == nil {
			binding, bindingErr := store.ReadReviewBinding(task.MissionID, task.ID, status.Attempt)
			if bindingErr == nil {
				status.BridgeRunning, _ = reviewbridge.Running(home, reviewbridge.Expected(home, binding))
			}
		}
	}
	if *jsonOutput {
		return encode(status)
	}
	fmt.Printf("task %s attempt %d review %s (%s), cursor %d\n", status.TaskID, status.Attempt, status.State, status.Posture, status.Cursor)
	return nil
}

func reviewFeedback(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("review feedback", flag.ContinueOnError)
	after := flags.Int("after", 0, "return feedback after this durable sequence")
	limit := flags.Int("limit", 20, "maximum feedback submissions (1-100)")
	attempt := flags.Int("attempt", 0, "current or historical task attempt (defaults to current)")
	jsonOutput := flags.Bool("json", false, "emit bounded structured feedback JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review feedback requires exactly one task ID")
	}
	result, err := flow.New(flow.Deps{}).ReviewFeedback(positional[0], *after, *limit, *attempt)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(result)
	}
	fmt.Printf("task %s attempt %d exact review %s..%s; %d feedback submission(s), cursor %d\n",
		result.TaskID, result.Attempt, result.BaseSHA[:10], result.HeadSHA[:10], len(result.Events), result.Cursor)
	for _, event := range result.Events {
		fmt.Printf("sequence %d: %d comment(s); use --json to read bounded untrusted comment data\n", event.Sequence, len(event.Comments))
	}
	return nil
}

func reviewClassify(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("review classify", flag.ContinueOnError)
	sequence := flags.Int("sequence", 0, "exact feedback sequence")
	disposition := flags.String("disposition", "", "requested-changes|non-actionable")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review classify requires exactly one task ID")
	}
	record, err := defaultTools().flow().ClassifyReviewFeedback(ctx, positional[0], *sequence, *disposition)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(record)
	}
	fmt.Printf("task %s review feedback sequence %d classified %s\n", record.TaskID, record.Sequence, record.Disposition)
	return nil
}

func reviewApply(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("review apply", flag.ContinueOnError)
	sequence := flags.Int("sequence", 0, "exact requested-change feedback sequence")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	tools.bind(flags, "herdr", "treehouse", "git", "gh-axi", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review apply requires exactly one task ID")
	}
	record, err := tools.flow().ApplyReviewFeedback(ctx, positional[0], *sequence)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(record)
	}
	fmt.Printf("task %s review feedback sequence %d routed from attempt %d to correction revision %d attempt %d\n",
		record.TaskID, record.Sequence, record.Attempt, record.TargetRevision, record.TargetAttempt)
	return nil
}

func reviewAcknowledge(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("review acknowledge", flag.ContinueOnError)
	sequence := flags.Int("sequence", 0, "exact approval sequence")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review acknowledge requires exactly one task ID")
	}
	record, err := flow.New(flow.Deps{}).AcknowledgeReviewApproval(ctx, positional[0], *sequence)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(record)
	}
	fmt.Printf("task %s exact head %s approval sequence %d acknowledged; delivery still requires separate confirmation\n",
		record.TaskID, record.HeadSHA[:10], record.Sequence)
	return nil
}

func reviewReconcile(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("review reconcile", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	tools.bind(flags, "read-the-code", "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review reconcile requires exactly one task ID")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	task, err := store.FindTask(positional[0])
	if err != nil {
		return err
	}
	binding, err := store.ReadReviewBinding(task.MissionID, task.ID, task.CurrentAttempt)
	if err != nil {
		return err
	}
	// Manual reconciliation and the long-lived bridge share the same kernel
	// owner. A live bridge remains the sole poll consumer; a crashed/missing
	// bridge leaves the lock free so this command catches up durably.
	lease, acquired, err := reviewbridge.Acquire(home, reviewbridge.Expected(home, binding))
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("the exact review bridge currently owns blocking poll; no concurrent reconcile consumer was started")
	}
	result, err := tools.flow().ReviewReconcile(ctx, positional[0])
	if err != nil {
		_ = lease.Release()
		return err
	}
	if len(result.Ingested) > 0 {
		notifyDurableChange(ctx, tools.flow(), result.TaskID, result.Attempt, monitor.ChangeReview)
	}
	if err := lease.Release(); err != nil {
		return err
	}
	if !result.Ended {
		if err := ensureReviewBridge(result.TaskID, result.Attempt, tools); err != nil {
			return err
		}
	}
	if *jsonOutput {
		return encode(result)
	}
	fmt.Printf("task %s review reconciled through cursor %d (%d new event(s))\n", result.TaskID, result.Cursor, len(result.Ingested))
	return nil
}

func reviewEnd(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("review end", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	tools.bind(flags, "read-the-code", "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("review end requires exactly one task ID")
	}
	result, err := tools.flow().ReviewEnd(ctx, positional[0])
	if err != nil {
		return err
	}
	if len(result.Ingested) > 0 {
		notifyDurableChange(ctx, tools.flow(), result.TaskID, result.Attempt, monitor.ChangeReview)
	}
	if *jsonOutput {
		return encode(result)
	}
	fmt.Printf("ended task %s review at durable cursor %d; canonical evidence was preserved\n", result.TaskID, result.Cursor)
	return nil
}

func ensureReviewBridge(taskID string, attempt int, tools toolConfig) error {
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	task, err := store.FindTask(taskID)
	if err != nil {
		return err
	}
	binding, err := store.ReadReviewBinding(task.MissionID, taskID, attempt)
	if err != nil {
		return err
	}
	expected := reviewbridge.Expected(home, binding)
	if running, err := reviewbridge.Running(home, expected); err != nil {
		return err
	} else if running {
		return nil
	}
	if strings.TrimSpace(tools.readCode) == "" {
		return errors.New("Read the Code executable is not configured; use --read-the-code or SOPHON_READ_THE_CODE")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	command := exec.Command(executable, "review", "bridge", taskID, "--attempt", fmt.Sprint(attempt),
		"--read-the-code", tools.readCode, "--herdr", tools.herdr, "--herdr-session", tools.herdrSession)
	command.Env = assignedDataHomeEnv(os.Environ(), home)
	command.Stdin, command.Stdout, command.Stderr = nil, devNull, devNull
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start review bridge: %w", err)
	}
	_ = command.Process.Release()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		running, checkErr := reviewbridge.Running(home, expected)
		if checkErr != nil {
			return checkErr
		}
		if running {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("review bridge child did not acquire the exact owner within 3s")
}

func reviewBridge(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("review bridge", flag.ContinueOnError)
	attempt := flags.Int("attempt", 0, "exact attempt")
	tools.bind(flags, "read-the-code", "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || *attempt < 1 {
		return errors.New("review bridge requires one task ID and a positive --attempt")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	task, err := store.FindTask(positional[0])
	if err != nil || task.CurrentAttempt != *attempt {
		return errors.New("review bridge target is not the exact current task attempt")
	}
	binding, err := store.ReadReviewBinding(task.MissionID, task.ID, *attempt)
	if err != nil {
		return err
	}
	lease, acquired, err := reviewbridge.Acquire(home, reviewbridge.Expected(home, binding))
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer lease.Release()
	signalCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	product := readcode.Client{Binary: tools.readCode}
	for signalCtx.Err() == nil {
		current, currentErr := reviewBridgeCurrent(task.ID, *attempt, binding)
		if currentErr != nil || !current {
			return currentErr
		}
		cursor, err := store.ReviewCursor(task.MissionID, task.ID, *attempt)
		if err != nil {
			return err
		}
		polled, err := product.Poll(signalCtx, binding.SessionID, cursor, 30*time.Second)
		if err != nil {
			return err
		}
		current, currentErr = reviewBridgeCurrent(task.ID, *attempt, binding)
		if currentErr != nil || !current {
			return currentErr
		}
		if len(polled.Events) > 0 {
			ingested, ended, err := tools.flow().IngestReviewEvents(signalCtx, task.ID, *attempt, binding, polled.Events)
			if err != nil {
				return err
			}
			if len(ingested) > 0 {
				notifyDurableChange(signalCtx, tools.flow(), task.ID, *attempt, monitor.ChangeReview)
			}
			if ended {
				return nil
			}
			cursor = polled.NextCursor
		}
		productStatus, err := product.Status(signalCtx, binding.SessionID)
		if err != nil {
			return err
		}
		if productStatus.Status != "open" || productStatus.Stale || productStatus.ApprovalStale ||
			productStatus.BaseSHA != binding.BaseSHA || productStatus.HeadSHA != binding.HeadSHA ||
			productStatus.LastSequence != cursor {
			return fmt.Errorf("%w: blocking bridge observed product session/revision/cursor drift", flow.ErrReviewReconcile)
		}
	}
	return signalCtx.Err()
}

func reviewBridgeCurrent(taskID string, attempt int, binding store.ReviewBinding) (bool, error) {
	task, err := store.FindTask(taskID)
	if err != nil || task.CurrentAttempt != attempt {
		return false, nil
	}
	current, err := store.ReadReviewBinding(task.MissionID, task.ID, attempt)
	if err != nil {
		return false, err
	}
	if current.SessionID != binding.SessionID || current.BaseSHA != binding.BaseSHA || current.HeadSHA != binding.HeadSHA ||
		current.TaskID != binding.TaskID || current.Attempt != binding.Attempt {
		return false, errors.New("review bridge canonical binding was replaced or conflicted")
	}
	if _, err := store.ReadRelease(task.MissionID, task.ID, attempt); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if delivery, err := store.ReadDelivery(task.MissionID, task.ID, attempt); err == nil && delivery.State.Terminal() {
		return false, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	return !reviewEndedForCLI(task), nil
}

func reviewEndedForCLI(task store.Task) bool {
	events, err := store.ReadReviewEvents(task.MissionID, task.ID, task.CurrentAttempt)
	return err == nil && len(events) > 0 && events[len(events)-1].Type == "end"
}

type monitorForwarder struct{ flow *flow.Flow }

func (f monitorForwarder) Forward(ctx context.Context, event monitor.Event) error {
	if event.Kind == monitor.MethodProgress {
		return f.flow.NotifyCommanderProgress(ctx, event.TaskID, event.Attempt, event.Phase, event.Note)
	}
	return f.flow.NotifyCommanderChange(ctx, event.TaskID, event.Attempt, event.Change)
}

func monitorCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &exitError{2, errors.New("expected: sophon monitor run|start|status|stop")}
	}
	switch args[0] {
	case "run":
		return monitorRun(ctx, args[1:])
	case "start":
		return monitorStart(ctx, args[1:])
	case "status":
		return monitorStatus(ctx, args[1:])
	case "stop":
		return monitorStop(ctx, args[1:])
	default:
		return &exitError{2, fmt.Errorf("unknown monitor command %q", args[0])}
	}
}

func monitorRun(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("monitor run", flag.ContinueOnError)
	backgroundChild := flags.Bool("background-child", false, "internal background child marker")
	tools.bind(flags, "herdr")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("monitor run does not accept positional arguments")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	signalCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var logOutput io.Writer = os.Stderr
	if *backgroundChild {
		logOutput = &boundedLogWriter{path: monitor.LogPath(home), limit: 256 << 10}
	}
	logger := log.New(logOutput, "sophon-monitor: ", log.LstdFlags|log.LUTC)
	server := &monitor.Server{Home: home, Forwarder: monitorForwarder{flow: tools.flow()}, Logger: logger}
	err = server.Run(signalCtx)
	if *backgroundChild && errors.Is(err, monitor.ErrAlreadyRunning) {
		return nil
	}
	return err
}

func monitorStart(_ context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("monitor start", flag.ContinueOnError)
	tools.bind(flags, "herdr")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("monitor start does not accept positional arguments")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	if _, err := monitor.NewClient(home).Ping(); err == nil {
		return encode(monitor.PublicState(home))
	}
	if err := monitor.EnsureRuntimeDir(home); err != nil {
		return err
	}
	logFile, err := openMonitorLog(home)
	if err != nil {
		return err
	}
	defer logFile.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := launchMonitorChild(executable, home, tools.herdr, logFile); err != nil {
		return err
	}
	launches := 1
	lastLaunch := time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := monitor.NewClient(home).Ping(); err == nil {
			return encode(monitor.PublicState(home))
		}
		// A start racing an authenticated stop can lose its first child to the
		// still-live old generation. Once exact cleanup reaches stopped, launch
		// a fresh contender; the startup lock still converges concurrent callers.
		if launches < 3 && time.Since(lastLaunch) >= 750*time.Millisecond && monitor.PublicState(home).Status == "stopped" {
			if err := launchMonitorChild(executable, home, tools.herdr, logFile); err != nil {
				return err
			}
			launches++
			lastLaunch = time.Now()
		}
		time.Sleep(40 * time.Millisecond)
	}
	return errors.New("monitor background process did not pass authenticated ping within 5s")
}

func launchMonitorChild(executable, home, herdrBinary string, logFile *os.File) error {
	command := exec.Command(executable, "monitor", "run", "--background-child", "--herdr", herdrBinary)
	command.Env = assignedDataHomeEnv(os.Environ(), home)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start monitor process: %w", err)
	}
	_ = command.Process.Release()
	return nil
}

func monitorStatus(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("monitor status", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit versioned JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("monitor status does not accept positional arguments")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	status := monitor.PublicState(home)
	if *jsonOutput {
		return encode(status)
	}
	fmt.Printf("monitor %s (protocol %d)\n", status.Status, status.ProtocolVersion)
	return nil
}

func monitorStop(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("monitor stop", flag.ContinueOnError)
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("monitor stop does not accept positional arguments")
	}
	home, err := datahome.AbsDir()
	if err != nil {
		return err
	}
	ack, err := monitor.NewClient(home).Shutdown()
	if err != nil {
		return err
	}
	if ack.Status != monitor.AckAccepted {
		return fmt.Errorf("monitor shutdown %s: %s", ack.Status, ack.Detail)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := monitor.PublicState(home)
		_, runtimeErr := os.Lstat(monitor.RuntimePath(home))
		_, socketErr := os.Lstat(monitor.SocketPath(home))
		if !status.Running && status.Status == "stopped" && os.IsNotExist(runtimeErr) && os.IsNotExist(socketErr) {
			return encode(ack)
		}
		time.Sleep(40 * time.Millisecond)
	}
	return errors.New("authenticated monitor shutdown was acknowledged but exact runtime files remain")
}

func openMonitorLog(home string) (*os.File, error) {
	path := monitor.LogPath(home)
	if err := validateMonitorLogTarget(path); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 256<<10 {
		_ = os.Remove(path + ".1")
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, fmt.Errorf("rotate monitor log: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open monitor log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect monitor log: %w", err)
	}
	return file, nil
}

func validateMonitorLogTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect monitor log: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("monitor log path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("monitor log is not user-private")
	}
	return nil
}

type boundedLogWriter struct {
	mu    sync.Mutex
	path  string
	limit int64
}

func (w *boundedLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := validateMonitorLogTarget(w.path); err != nil {
		return 0, err
	}
	if info, err := os.Stat(w.path); err == nil && info.Size()+int64(len(data)) > w.limit {
		_ = os.Remove(w.path + ".1")
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return 0, err
		}
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return written, writeErr
	}
	return written, closeErr
}

func assignedDataHomeEnv(existing []string, home string) []string {
	prefix := datahome.OverrideEnv + "="
	result := make([]string, 0, len(existing)+1)
	for _, value := range existing {
		if !strings.HasPrefix(value, prefix) {
			result = append(result, value)
		}
	}
	return append(result, prefix+home)
}

func notifyDurableChange(ctx context.Context, f *flow.Flow, taskID string, attempt int, change string) {
	home, err := datahome.AbsDir()
	if err == nil {
		var generation string
		generation, err = monitor.CanonicalGeneration(home, taskID, attempt, change)
		if err == nil {
			var ack monitor.Ack
			ack, err = monitor.NewClient(home).TaskChanged(taskID, attempt, change, generation)
			if err == nil && (ack.Status == monitor.AckAccepted || ack.Status == monitor.AckCoalesced) {
				return
			}
			if err == nil {
				err = fmt.Errorf("monitor %s: %s", ack.Status, ack.Detail)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sophon: notification monitor unavailable (durable %s is unaffected): %v\n", change, err)
	}
	if directErr := f.NotifyCommanderChange(ctx, taskID, attempt, change); directErr != nil {
		fmt.Fprintf(os.Stderr, "sophon: commander wake undelivered (durable %s is unaffected): %v\n", change, directErr)
	}
}

func verifyCompleteCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("verify-complete", flag.ContinueOnError)
	tools.bind(flags, "git", "treehouse", "herdr")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("verify-complete requires exactly one task ID")
	}
	outcome, err := tools.flow().VerifyComplete(ctx, positional[0])
	if err != nil {
		return err
	}
	if err := encode(outcome); err != nil {
		return err
	}
	notifyDurableChange(ctx, tools.flow(), positional[0], outcome.Attempt, monitor.ChangeVerification)
	// Successful verification of a no-validation task is terminal worker
	// evidence; retirement is quiet cleanup and never a verification failure.
	return retireWorkerQuietly(ctx, tools.flow(), positional[0])
}

func validateCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	tools.bind(flags, "git", "herdr")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("validate requires exactly one task ID")
	}
	record, err := tools.flow().Validate(ctx, positional[0])
	if err != nil {
		return err
	}
	if err := encode(record); err != nil {
		return err
	}
	notifyDurableChange(ctx, tools.flow(), positional[0], record.Attempt, monitor.ChangeValidation)
	if !record.Passed {
		return errors.New("validation failed")
	}
	// A passing validation is terminal worker evidence; retire the pane.
	return retireWorkerQuietly(ctx, tools.flow(), positional[0])
}

// retireWorkerQuietly runs terminal-evidence worker pane retirement as
// bounded cleanup: failure is a stderr diagnostic and never rewrites the
// successful verification or validation it follows.
func retireWorkerQuietly(ctx context.Context, f *flow.Flow, taskID string) error {
	if err := f.RetireWorker(ctx, taskID); err != nil {
		fmt.Fprintf(os.Stderr, "sophon: worker pane cleanup incomplete (durable evidence is unaffected): %v\n", err)
	}
	return nil
}

func deliverCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("deliver", flag.ContinueOnError)
	confirmed := flags.Bool("confirmed", false, "operator confirmation for the delivery effect")
	tools.bind(flags, "git", "gh-axi", "read-the-code")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("deliver requires exactly one task ID")
	}
	delivered, err := tools.flow().Deliver(ctx, positional[0], *confirmed)
	if err != nil {
		return err
	}
	notifyDurableChange(ctx, tools.flow(), positional[0], delivered.Attempt, monitor.ChangeDelivery)
	return encode(delivered)
}

func deliverySelectionCommand(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "select" {
		return &exitError{2, errors.New("expected: sophon delivery select")}
	}
	tools := defaultTools()
	flags := flag.NewFlagSet("delivery select", flag.ContinueOnError)
	mode := flags.String("mode", "", "public delivery mode (branch|pr)")
	title := flags.String("title", "", "public-safe title")
	branch := flags.String("branch", "", "public-safe branch")
	confirmed := flags.Bool("confirmed", false, "confirm this exact local-to-public selection (no delivery effect)")
	tools.bind(flags, "git")
	positional, err := parseFlags(flags, args[1:])
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("delivery select requires exactly one task ID")
	}
	selection, err := tools.flow().SelectDelivery(ctx, positional[0], domain.DeliveryMode(*mode), *title, *branch, *confirmed)
	if err != nil {
		return err
	}
	return encode(selection)
}

func releaseCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	attempt := flags.Int("attempt", 0, "exact attempt copy to release (default current)")
	tools.bind(flags, "treehouse")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("release requires exactly one task ID")
	}
	released, err := tools.flow().ReleaseLeaseAttempt(ctx, positional[0], *attempt)
	if err != nil {
		return err
	}
	notifyDurableChange(ctx, tools.flow(), positional[0], released.Attempt, monitor.ChangeRelease)
	return encode(released)
}

func statusCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	all := flags.Bool("all", false, "include released task and mission history")
	tools.bind(flags, "herdr", "herdr-session", "git", "gh-axi")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("status does not accept positional arguments")
	}
	report, err := tools.flow().Status(ctx, *all)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return encode(report)
	}
	fmt.Println("PROJECT\tMISSION\tTASK\tSTATE\tATTEMPT\tDETAIL")
	for _, mission := range report.Missions {
		project := mission.Mission.ProjectKey
		if project == "" {
			project = filepath.Base(mission.Mission.ProjectPath)
		}
		for _, task := range mission.Tasks {
			fmt.Printf("%s\t%s\t%s\t%s\t%d\t%s\n", project, mission.Mission.ID, task.Task.ID, task.State, task.Attempt, task.Detail)
			if *all {
				for _, revision := range task.Revisions {
					for _, attempt := range revision.Attempts {
						fmt.Printf("REVISION\t%s\t%d\t%d\t%s\t%s\n", task.Task.ID, revision.Revision, attempt.Attempt, attempt.State, attempt.Detail)
					}
				}
			}
		}
	}
	// The action queue is the primary output: a commander drains every listed
	// command, re-derives, and repeats until the queue is empty before it
	// reports or waits.
	for _, action := range report.Actions {
		fmt.Printf("ACTION\t%s\n", action.Command)
	}
	return nil
}

func sendCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	tools.bind(flags, "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return errors.New("send requires a task ID and a message")
	}
	if err := tools.flow().Send(ctx, positional[0], positional[1]); err != nil {
		return err
	}
	return encode(map[string]any{"task_id": positional[0], "sent": true})
}

func promptCommand(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "commander" {
		return promptCommander(ctx, args[1:])
	}
	return &exitError{2, errors.New("expected: sophon prompt commander")}
}

func promptCommander(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("prompt commander", flag.ContinueOnError)
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("prompt commander does not accept positional arguments")
	}
	home, err := datahome.Dir()
	if err != nil {
		return err
	}
	promptID, err := id.New("commander-prompt")
	if err != nil {
		return err
	}
	skillDir := filepath.Join(home, "skills", "commander", promptID)
	if err := runtimeprompts.MaterializeSkills(skillDir, runtimeprompts.CommanderSkills); err != nil {
		return fmt.Errorf("materialize commander skills: %w", err)
	}
	body, err := runtimeprompts.Compose(skillDir)
	if err != nil {
		return err
	}
	fmt.Print(body)
	return nil
}

func encode(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  sophon version
  sophon workspace init|inspect ROOT
  sophon project list --workspace ROOT
  sophon project create KEY --workspace ROOT [--initial-branch BRANCH]
  sophon project clone KEY --workspace ROOT --source GIT_SOURCE
  sophon project add|inspect KEY --workspace ROOT
  sophon project publish KEY --workspace ROOT --repository OWNER/REPO --remote-url URL --visibility private|public|internal --confirmed [--gh-axi BIN]
  sophon mission create --workspace ROOT --project KEY --title TITLE --objective OBJECTIVE [--git BIN]
  sophon mission create --project LEGACY_PATH --title TITLE --objective OBJECTIVE
  sophon mission list [--json]
  sophon task create --mission ID --title TITLE --objective WORKER_OBJECTIVE [--kind KIND] [--delivery local|branch|pr] [--delivery-branch PUBLIC_BRANCH] [--validate COMMAND] [--review off|optional|required]
  sophon task cancel TASK --reason REASON --confirmed
  sophon task revise TASK --title TITLE --objective OBJECTIVE --confirmed [--validate COMMAND]
  sophon spawn TASK [--retry] [--herdr BIN] [--treehouse BIN] [--git BIN] [--herdr-session NAME]
  sophon revise TASK --reason REASON --objective CORRECTION [--accept-external-head] [--herdr BIN] [--treehouse BIN] [--git BIN] [--gh-axi BIN] [--herdr-session NAME]
  sophon worker complete TASK --attempt N --head-sha SHA --result FILE [--git BIN] [--herdr BIN]
  sophon worker report TASK --attempt N --head-sha SHA --report FILE [--git BIN] [--herdr BIN]
  sophon worker progress TASK --attempt N --phase PHASE [--message NOTE]
  sophon commander attach [--scope ROOT] [--pane ID] [--workspace ID] [--tab ID] [--herdr BIN] [--herdr-session NAME]
  sophon monitor run|start [--herdr BIN]
  sophon monitor status [--json]
  sophon monitor stop
  sophon review set TASK --posture optional|required [--json]
  sophon review open TASK [--attempt N] [--no-browser] [--json] [--read-the-code BIN]
  sophon review status TASK [--json]
  sophon review feedback TASK [--attempt N] [--after N] [--limit N] [--json]
  sophon review classify TASK --sequence N --disposition requested-changes|non-actionable [--json]
  sophon review apply TASK --sequence N [--json] [--herdr BIN] [--treehouse BIN] [--git BIN] [--gh-axi BIN] [--herdr-session NAME]
  sophon review acknowledge TASK --sequence N [--json]
  sophon review reconcile TASK [--json] [--read-the-code BIN]
  sophon review end TASK [--json] [--read-the-code BIN]
  sophon verify-complete TASK [--git BIN] [--treehouse BIN] [--herdr BIN]
  sophon validate TASK [--git BIN] [--herdr BIN]
  sophon deliver TASK --confirmed [--git BIN] [--gh-axi BIN] [--read-the-code BIN]
  sophon delivery select TASK --mode branch|pr --title PUBLIC_TITLE --branch PUBLIC_BRANCH --confirmed [--git BIN]
  sophon release TASK [--attempt N] [--treehouse BIN]
  sophon status [--json] [--all] [--herdr BIN] [--git BIN] [--gh-axi BIN] [--herdr-session NAME]
  sophon send TASK MESSAGE [--herdr BIN] [--herdr-session NAME]
  sophon prompt commander`)
}
