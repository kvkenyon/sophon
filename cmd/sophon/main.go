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
	"sophon/internal/store"
	runtimeprompts "sophon/prompts"
)

const version = "0.4.0-m1"

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
	case "task":
		return taskCommand(ctx, args[1:])
	case "spawn":
		return spawnCommand(ctx, args[1:])
	case "worker":
		return workerCommand(ctx, args[1:])
	case "commander":
		return commanderCommand(ctx, args[1:])
	case "monitor":
		return monitorCommand(ctx, args[1:])
	case "verify-complete":
		return verifyCompleteCommand(ctx, args[1:])
	case "validate":
		return validateCommand(ctx, args[1:])
	case "deliver":
		return deliverCommand(ctx, args[1:])
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
	herdrSession string
}

func defaultTools() toolConfig {
	session := strings.TrimSpace(os.Getenv("HERDR_SESSION"))
	if session == "" {
		session = "default"
	}
	return toolConfig{herdr: "herdr", treehouse: "treehouse", git: "git", ghAxi: "gh-axi", herdrSession: session}
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
		case "herdr-session":
			flags.StringVar(&t.herdrSession, "herdr-session", t.herdrSession, "Herdr session name (env HERDR_SESSION)")
		}
	}
}

func (t toolConfig) flow() *flow.Flow {
	panes := herdr.NewCommandAdapter(t.herdr, t.herdrSession, "sophon")
	deps := flow.ProductionDeps(t.git, t.treehouse, t.ghAxi, panes)
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
	project := flags.String("project", "", "project repository path")
	title := flags.String("title", "", "mission title")
	objective := flags.String("objective", "", "mission objective")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("mission create does not accept positional arguments")
	}
	created, err := flow.New(flow.Deps{}).CreateMission(ctx, *project, *title, *objective)
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
		fmt.Printf("%s\t%s\t%s\t%s\n", mission.ID, mission.Title, mission.ProjectPath, mission.CreatedAt.Format("2006-01-02"))
	}
	return nil
}

func taskCommand(ctx context.Context, args []string) error {
	if len(args) >= 1 && args[0] == "create" {
		return taskCreate(ctx, args[1:])
	}
	return &exitError{2, errors.New("expected: sophon task create")}
}

func taskCreate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("task create", flag.ContinueOnError)
	missionID := flags.String("mission", "", "mission ID")
	title := flags.String("title", "", "concise public task and pull request title")
	objective := flags.String("objective", "", "detailed worker objective")
	deliveryBranch := flags.String("delivery-branch", "", "explicit public-safe branch to push")
	kind := flags.String("kind", string(domain.TaskImplementation), "task kind")
	delivery := flags.String("delivery", string(domain.DeliveryBranch), "delivery mode (branch|pr)")
	validate := flags.String("validate", "", "required validation command")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("task create does not accept positional arguments")
	}
	created, err := flow.New(flow.Deps{}).CreateTask(ctx, *missionID, *title, *objective, *deliveryBranch,
		domain.TaskKind(*kind), domain.DeliveryMode(*delivery), *validate)
	if err != nil {
		return err
	}
	return encode(created)
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
	tools.bind(flags, "herdr", "herdr-session")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("commander attach does not accept positional arguments")
	}
	registration, err := tools.flow().AttachCommander(ctx, flow.AttachRequest{
		Session: tools.herdrSession, WorkspaceID: *workspace, TabID: *tab, PaneID: *pane})
	if err != nil {
		return err
	}
	return encode(registration)
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
	tools.bind(flags, "git", "gh-axi")
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

func releaseCommand(ctx context.Context, args []string) error {
	tools := defaultTools()
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	tools.bind(flags, "treehouse")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("release requires exactly one task ID")
	}
	released, err := tools.flow().ReleaseLease(ctx, positional[0])
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
	tools.bind(flags, "herdr", "herdr-session")
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
	fmt.Println("MISSION\tTASK\tSTATE\tATTEMPT\tDETAIL")
	for _, mission := range report.Missions {
		for _, task := range mission.Tasks {
			fmt.Printf("%s\t%s\t%s\t%d\t%s\n", mission.Mission.ID, task.Task.ID, task.State, task.Attempt, task.Detail)
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
  sophon mission create --project PATH --title TITLE --objective OBJECTIVE
  sophon mission list [--json]
  sophon task create --mission ID --title PUBLIC_TITLE --objective WORKER_OBJECTIVE --delivery-branch PUBLIC_BRANCH [--kind KIND] [--delivery branch|pr] [--validate COMMAND]
  sophon spawn TASK [--retry] [--herdr BIN] [--treehouse BIN] [--git BIN] [--herdr-session NAME]
  sophon worker complete TASK --attempt N --head-sha SHA --result FILE [--git BIN] [--herdr BIN]
  sophon worker report TASK --attempt N --head-sha SHA --report FILE [--git BIN] [--herdr BIN]
  sophon worker progress TASK --attempt N --phase PHASE [--message NOTE]
  sophon commander attach [--pane ID] [--workspace ID] [--tab ID] [--herdr BIN] [--herdr-session NAME]
  sophon monitor run|start [--herdr BIN]
  sophon monitor status [--json]
  sophon monitor stop
  sophon verify-complete TASK [--git BIN] [--treehouse BIN] [--herdr BIN]
  sophon validate TASK [--git BIN] [--herdr BIN]
  sophon deliver TASK --confirmed [--git BIN] [--gh-axi BIN]
  sophon release TASK [--treehouse BIN]
  sophon status [--json] [--all] [--herdr BIN] [--herdr-session NAME]
  sophon send TASK MESSAGE [--herdr BIN] [--herdr-session NAME]
  sophon prompt commander`)
}
