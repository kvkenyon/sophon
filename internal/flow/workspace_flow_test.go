package flow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sophon/internal/delivery"
	"sophon/internal/domain"
	gitcontrol "sophon/internal/git"
	"sophon/internal/store"
	"sophon/internal/workspace"
)

type scriptedBootstrap struct {
	state  gitcontrol.BootstrapState
	result gitcontrol.BootstrapResult
	err    error
}

func (b *scriptedBootstrap) InspectBootstrap(context.Context, string) (gitcontrol.BootstrapState, error) {
	return b.state, nil
}

func (b *scriptedBootstrap) CreateBootstrap(context.Context, string, gitcontrol.BootstrapSpec) (gitcontrol.BootstrapResult, error) {
	return b.result, b.err
}

func flowGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestWorkspaceMissionEmptyBootstrapAndTruthfulStart(t *testing.T) {
	home := useHome(t)
	root := filepath.Join(t.TempDir(), "workspace")
	if _, err := workspace.Init(root); err != nil {
		t.Fatal(err)
	}
	projects := workspace.Inspector{GitBinary: "git"}
	project, err := projects.Create(context.Background(), root, "empty-local", "trunk")
	if err != nil {
		t.Fatal(err)
	}
	rig := newRig()
	bootstrap := gitcontrol.NewClient()
	rig.flow = New(Deps{Git: rig.git, Bootstrap: bootstrap, Projects: projects, Leases: rig.leases, Panes: rig.panes,
		DeliveryGit: rig.delGit, DeliveryRemote: rig.remote, NewValidator: func(string) Validator { return rig.validate }})
	mission, err := rig.flow.CreateWorkspaceMission(context.Background(), root, project.Key, "Local outcome", "Build the local outcome")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(context.Background(), mission.ID, "Build local outcome", "Implement and test it.", "", "", domain.DeliveryLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	report, err := rig.flow.Status(context.Background())
	if err != nil || report.Missions[0].Tasks[0].State != store.StatePlanned || len(report.Actions) != 1 || report.Actions[0].Kind != ActionStart {
		t.Fatalf("planned status = %+v, %v", report, err)
	}
	spawn, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.ReadBootstrapReceipt(mission.ID, task.ID)
	if err != nil || receipt.CommitSHA == "" || receipt.CommitSHA != flowGit(t, project.Path, "rev-parse", "HEAD") {
		t.Fatalf("bootstrap receipt = %+v, %v", receipt, err)
	}
	if flowGit(t, project.Path, "rev-list", "--parents", "-n", "1", "HEAD") != receipt.CommitSHA ||
		flowGit(t, project.Path, "ls-tree", "--name-only", "HEAD") != "" || flowGit(t, project.Path, "remote") != "" {
		t.Fatal("bootstrap was not one remote-free empty root")
	}
	status, err := store.Derive(func() store.Task { value, _ := store.FindTask(task.ID); return value }())
	if err != nil || status.State != store.StateActive || spawn.Pane.PaneID == "" {
		t.Fatalf("active status = %+v, %v", status, err)
	}
	brief, err := os.ReadFile(store.AttemptPath(home, mission.ID, task.ID, 1, "brief.md"))
	if err != nil || !strings.Contains(string(brief), "Workspace project: `empty-local`") ||
		!strings.Contains(string(brief), "Development posture: `local`") || strings.Contains(string(brief), "Public delivery branch") {
		t.Fatalf("local brief: %v\n%s", err, brief)
	}
}

func TestInterruptedStartRemainsPlannedAndConvergesOnSameAttempt(t *testing.T) {
	useHome(t)
	rig := newRig()
	_, task := rig.createMissionAndTask(t, domain.DeliveryBranch, "")
	rig.leases.acquireErr = errors.New("allocation unavailable")
	if _, err := rig.flow.Spawn(context.Background(), task.ID, false); err == nil {
		t.Fatal("allocation failure was accepted")
	}
	current, _ := store.FindTask(task.ID)
	status, err := store.Derive(current)
	if err != nil || status.State != store.StatePlanned || current.CurrentAttempt != 1 {
		t.Fatalf("failed start = %+v current=%+v err=%v", status, current, err)
	}
	rig.leases.acquireErr = nil
	spawn, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil || spawn.Attempt != 1 {
		t.Fatalf("restart convergence = %+v, %v", spawn, err)
	}
}

func TestBootstrapIntentAndCommitCrashRecovery(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	mission, err := rig.flow.CreateMission(context.Background(), "/empty", "Bootstrap", "Start empty local work")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(context.Background(), mission.ID, "Bootstrap local", "Implement locally.", "", "", domain.DeliveryLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := &scriptedBootstrap{state: gitcontrol.BootstrapState{Needed: true, Branch: "main", Ref: "refs/heads/main"},
		err: errors.New("simulated death after intent")}
	rig.flow = New(Deps{Git: rig.git, Bootstrap: bootstrap, Leases: rig.leases, Panes: rig.panes})
	if _, err := rig.flow.Spawn(context.Background(), task.ID, false); err == nil {
		t.Fatal("simulated bootstrap crash succeeded")
	}
	intent, err := store.ReadBootstrapIntent(mission.ID, task.ID)
	if err != nil || intent.Ref != "refs/heads/main" {
		t.Fatalf("intent before mutation = %+v, %v", intent, err)
	}
	if _, err := store.ReadBootstrapReceipt(mission.ID, task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed bootstrap published receipt: %v", err)
	}
	current, _ := store.FindTask(task.ID)
	if current.CurrentAttempt != 0 {
		t.Fatalf("bootstrap failure invented attempt %d", current.CurrentAttempt)
	}
	bootstrap.err = nil
	bootstrap.result = gitcontrol.BootstrapResult{Branch: "main", Ref: "refs/heads/main", CommitSHA: testBaseSHA}
	spawn, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil || spawn.Attempt != 1 {
		t.Fatalf("intent recovery = %+v, %v", spawn, err)
	}
	receipt, err := store.ReadBootstrapReceipt(mission.ID, task.ID)
	if err != nil || receipt.CommitSHA != testBaseSHA {
		t.Fatalf("recovered receipt = %+v, %v", receipt, err)
	}
	if _, err := os.Stat(store.BootstrapIntentPath(home, mission.ID, task.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapCommitWithoutReceiptRecoversExactRoot(t *testing.T) {
	home := useHome(t)
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	flowGit(t, project, "init", "-b", "main")
	client := gitcontrol.NewClient()
	rig := newRig()
	mission, err := rig.flow.CreateMission(context.Background(), project, "Recover", "Recover bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(context.Background(), mission.ID, "Recover local", "Implement locally.", "", "", domain.DeliveryLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	intent := store.BootstrapIntent{Version: 1, TaskID: task.ID, MissionID: mission.ID, ProjectPath: project,
		Branch: "main", Ref: "refs/heads/main", CommitMessage: "Initialize project history",
		AuthorName: "Project Contributors", AuthorEmail: "contributors@localhost.invalid", AuthoredAt: now, RequestedAt: now}
	if err := store.PublishImmutable(store.BootstrapIntentPath(home, mission.ID, task.ID), intent); err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateBootstrap(context.Background(), project, gitcontrol.BootstrapSpec{Branch: intent.Branch, Ref: intent.Ref,
		CommitMessage: intent.CommitMessage, AuthorName: intent.AuthorName, AuthorEmail: intent.AuthorEmail, AuthoredAt: intent.AuthoredAt})
	if err != nil {
		t.Fatal(err)
	}
	rig.flow = New(Deps{Git: rig.git, Bootstrap: client, Leases: rig.leases, Panes: rig.panes})
	if _, err := rig.flow.Spawn(context.Background(), task.ID, false); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.ReadBootstrapReceipt(mission.ID, task.ID)
	if err != nil || receipt.CommitSHA != created.CommitSHA {
		t.Fatalf("commit/receipt recovery = %+v, %v", receipt, err)
	}
}

func TestLocalDeliverySelectionIsSeparateAndImmutable(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	mission, err := rig.flow.CreateMission(context.Background(), "/repo", "Local", "Local implementation")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(context.Background(), mission.ID, "Private local title", "Implement locally.", "", "", domain.DeliveryLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	spawn, err := rig.flow.Spawn(context.Background(), task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	result := domain.WorkerResult{Version: 1, Status: "completed", Summary: "Implemented behavior",
		Verification: []domain.VerificationResult{{Command: "go test ./...", ExitCode: 0}}, ChangedFiles: []string{"feature.go"}, Risks: []string{}}
	data, _ := json.Marshal(result)
	if err := store.PublishBytes(store.AttemptPath(home, mission.ID, task.ID, 1, "result.json"), data); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(store.AttemptPath(home, mission.ID, task.ID, 1, "outcome.json"), store.Outcome{
		TaskID: task.ID, Attempt: 1, Revision: 1, HeadSHA: testHeadSHA, Branch: spawn.Branch,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Deliver(context.Background(), task.ID, true); err == nil || !strings.Contains(err.Error(), "local completion is not delivery") {
		t.Fatalf("local deliver error = %v", err)
	}
	if _, err := rig.flow.SelectDelivery(context.Background(), task.ID, domain.DeliveryPR, "Add product behavior", "feature/product-behavior", false); err == nil {
		t.Fatal("delivery selection without confirmation succeeded")
	}
	selection, err := rig.flow.SelectDelivery(context.Background(), task.ID, domain.DeliveryPR, "Add product behavior", "feature/product-behavior", true)
	if err != nil || selection.Repository != testRepo || rig.remote.pushes != 0 || rig.remote.creates != 0 {
		t.Fatalf("selection = %+v, err=%v pushes=%d creates=%d", selection, err, rig.remote.pushes, rig.remote.creates)
	}
	if _, err := rig.flow.SelectDelivery(context.Background(), task.ID, domain.DeliveryBranch, "Other", "other", true); err == nil {
		t.Fatal("differing delivery selection replaced immutable intent")
	}
	if _, err := rig.flow.Deliver(context.Background(), task.ID, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("delivery without second confirmation = %v", err)
	}
	rig.remote.create = delivery.PullRequest{Repository: testRepo, Branch: selection.PublicBranch,
		HeadSHA: testHeadSHA, URL: "https://github.com/acme/repo/pull/71", Number: 71}
	receipt, err := rig.flow.Deliver(context.Background(), task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != store.DeliveryDeliveredPR || receipt.PRNumber != 71 ||
		rig.remote.pushes != 1 || rig.remote.creates != 1 || rig.remote.input.Title != selection.PublicTitle {
		t.Fatalf("separately confirmed delivery = %+v pushes=%d creates=%d input=%+v", receipt, rig.remote.pushes, rig.remote.creates, rig.remote.input)
	}
}

func TestPlannedTaskCanBeCancelledOrReplacedWithoutWorker(t *testing.T) {
	home := useHome(t)
	rig := newRig()
	mission, err := rig.flow.CreateMission(context.Background(), "/repo", "Plan", "Authorized plan")
	if err != nil {
		t.Fatal(err)
	}
	cancelTask, err := rig.flow.CreateTask(context.Background(), mission.ID, "Cancel me", "Authorized but not started.", "", "", domain.DeliveryLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.CancelPlanned(context.Background(), cancelTask.ID, "Operator withdrew this exact outcome", false); err == nil {
		t.Fatal("unconfirmed cancellation succeeded")
	}
	if _, err := rig.flow.CancelPlanned(context.Background(), cancelTask.ID, "Operator withdrew this exact outcome", true); err != nil {
		t.Fatal(err)
	}
	operational, err := rig.flow.Status(context.Background())
	if err != nil || len(operational.Missions) != 0 {
		t.Fatalf("cancelled task leaked into operational status: %+v, %v", operational, err)
	}
	history, err := rig.flow.Status(context.Background(), true)
	if err != nil || history.Missions[0].Tasks[0].State != store.StateCancelled {
		t.Fatalf("cancellation history = %+v, %v", history, err)
	}

	prior, err := rig.flow.CreateTask(context.Background(), mission.ID, "Original plan", "Original accepted details.", "", "", domain.DeliveryLocal, "")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := rig.flow.RevisePlanned(context.Background(), prior.ID, "Revised plan", "Revised accepted details.", "go test ./...", true)
	if err != nil || replacement.ID == prior.ID || replacement.CurrentAttempt != 0 {
		t.Fatalf("replacement = %+v, %v", replacement, err)
	}
	cancelled, err := store.ReadCancellation(mission.ID, prior.ID)
	if err != nil || cancelled.Replacement != replacement.ID || cancelled.ReplacementTask == nil || cancelled.ReplacementTask.ID != replacement.ID {
		t.Fatalf("revision link = %+v, %v", cancelled, err)
	}
	// Simulate a crash after the immutable cancellation/replacement intent but
	// before replacement task publication. Repeating the same command restores
	// the exact preselected task rather than minting another identity.
	if err := os.Remove(store.TaskPath(home, mission.ID, replacement.ID)); err != nil {
		t.Fatal(err)
	}
	recovered, err := rig.flow.RevisePlanned(context.Background(), prior.ID, "Revised plan", "Revised accepted details.", "go test ./...", true)
	if err != nil || recovered.ID != replacement.ID || recovered.Title != "Revised plan" || recovered.Objective != "Revised accepted details." {
		t.Fatalf("planned revision recovery = %+v, %v", recovered, err)
	}
}
