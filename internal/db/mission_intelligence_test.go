package db

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sophon/internal/domain"
	"sophon/internal/knowledge"
	"sophon/internal/signals"
	"sophon/internal/validation"
)

func TestMissionDigestRegenerationStoresArtifact(t *testing.T) {
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	defer store.Close()
	task := intelligenceTask(t, store, mission.ID, "digest task")
	scout, err := store.CreateTask(context.Background(), "cmd_digest_scout", CreateTaskInput{
		MissionID: mission.ID, Kind: domain.TaskScout, Title: "completed scout", Objective: "report",
		DeliveryMode: domain.DeliveryGate,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, state := range []domain.TaskState{domain.TaskProvisioning, domain.TaskStarting, domain.TaskRunning, domain.TaskCollecting, domain.TaskReportReady} {
		scout = transitionTask(t, store, scout, state, "digest_scout_"+string(rune('a'+index)))
	}
	signal, err := store.CreateSignal(context.Background(), "cmd_digest_signal", CreateSignalInput{
		MissionID: mission.ID, TaskID: &task.ID, Kind: signals.SignalDecision,
		Question: "Keep compatibility?", Actor: "commander",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSignal(context.Background(), "cmd_digest_resolve", ResolveSignalInput{
		SignalID: signal.ID, ExpectedVersion: 1, Answer: "Keep the existing contract.", Actor: "operator",
	}); err != nil {
		t.Fatal(err)
	}

	artifact, err := store.RegenerateMissionDigest(context.Background(), "cmd_digest_explicit", RegenerateMissionDigestInput{
		MissionID: mission.ID, Actor: "operator", Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestMissionDigest(context.Background(), mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	content := string(artifact.Content)
	if latest.ID != artifact.ID || artifact.SHA256 == "" || artifact.BasedOnEventSequence == 0 ||
		!strings.Contains(content, "Keep the existing contract.") || !strings.Contains(content, "digest task (queued)") ||
		!strings.Contains(content, "completed scout (report_ready)") {
		t.Fatalf("artifact=%+v\n%s", artifact, content)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mission_digest_artifacts WHERE mission_id = ?`, mission.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 4 {
		t.Fatalf("digest artifacts=%d, want creation, task completion, signal resolution, and explicit regeneration", count)
	}
}

func TestKnowledgeCandidatePromotionPreservesProvenance(t *testing.T) {
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	defer store.Close()
	task := intelligenceTask(t, store, mission.ID, "knowledge task")
	evidence := domain.ArtifactID("art_evidence")
	if _, err := store.db.Exec(`INSERT INTO artifacts(id, task_id, attempt, kind, media_type, sha256, content, created_at)
		VALUES (?, ?, 1, 'worker.report', 'text/plain', 'abc', 'evidence', '2026-01-01T00:00:00.000Z')`, evidence, task.ID); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.ProposeKnowledge(context.Background(), "cmd_knowledge_propose", ProposeKnowledgeInput{
		ProjectID: mission.ProjectID, MissionID: &mission.ID, Scope: knowledge.ScopeLearned,
		Kind: "test-fixture", Content: "Reset Redis after recovery tests.", CreatedBy: "worker-7",
		Origin: knowledge.OriginAgent, TriggerTaskID: &task.ID, EvidenceArtifactID: &evidence, Confidence: .82,
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != knowledge.StatusCandidate || candidate.TriggerTaskID == nil || candidate.Confidence != .82 {
		t.Fatalf("candidate=%+v", candidate)
	}
	active, err := store.TransitionKnowledge(context.Background(), "cmd_knowledge_promote", TransitionKnowledgeInput{
		KnowledgeID: candidate.ID, To: knowledge.StatusActive, Actor: "commander", Origin: knowledge.OriginCommander,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != knowledge.StatusActive || active.CreatedBy != "worker-7" || active.Origin != knowledge.OriginAgent ||
		active.TriggerTaskID == nil || *active.TriggerTaskID != task.ID || active.EvidenceArtifactID == nil ||
		*active.EvidenceArtifactID != evidence {
		t.Fatalf("active=%+v", active)
	}
	if _, err := store.TransitionKnowledge(context.Background(), "cmd_agent_promote", TransitionKnowledgeInput{
		KnowledgeID: candidate.ID, To: knowledge.StatusActive, Actor: "worker-7", Origin: knowledge.OriginAgent,
	}); !errors.Is(err, knowledge.ErrPromotionAuthority) {
		t.Fatalf("agent promotion error=%v", err)
	}
}

func TestAgentCriticalPolicyWritesAreMechanicallyRefused(t *testing.T) {
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	defer store.Close()
	for name, policy := range map[string]struct {
		scope knowledge.Scope
		kind  string
	}{
		"immutable scope":  {knowledge.ScopeImmutablePolicy, "note"},
		"critical surface": {knowledge.ScopeLearned, "task state machine"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.ProposeKnowledge(context.Background(), domain.CommandID("cmd_refuse_"+strings.ReplaceAll(name, " ", "_")), ProposeKnowledgeInput{
				ProjectID: mission.ProjectID, Scope: policy.scope, Kind: policy.kind, Content: "weaken it",
				CreatedBy: "worker", Origin: knowledge.OriginAgent, Confidence: 1,
			})
			if !errors.Is(err, knowledge.ErrCriticalPolicyWrite) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	_, err := store.db.Exec(`INSERT INTO knowledge(id, project_id, scope, kind, content, created_by, origin,
		confidence, status, created_at) VALUES ('knw_bypass', ?, 'immutable-policy', 'note', 'bypass',
		'worker', 'agent', 1, 'candidate', '2026-01-01T00:00:00.000Z')`, mission.ProjectID)
	if err == nil || !strings.Contains(err.Error(), "critical-policy write refused") {
		t.Fatalf("SQLite defense-in-depth error=%v", err)
	}
}

func TestMissionBudgetDimensionsExpireToNeedsAttention(t *testing.T) {
	t.Run("wall clock", func(t *testing.T) {
		store, mission := intelligenceMission(t, domain.MissionBudget{MaxWallClock: time.Second})
		defer store.Close()
		task := intelligenceTask(t, store, mission.ID, "wall")
		result, err := store.EnforceMissionBudget(context.Background(), "cmd_wall", EnforceMissionBudgetInput{
			MissionID: mission.ID, ObservedAt: mission.CreatedAt.Add(time.Second), Actor: "test",
		})
		if err != nil || !result.Expired {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		assertNeedsAttention(t, store, task.ID)
	})
	t.Run("concurrent tasks", func(t *testing.T) {
		store, mission := intelligenceMission(t, domain.MissionBudget{MaxConcurrentTasks: 1})
		defer store.Close()
		first := intelligenceTask(t, store, mission.ID, "first")
		second := intelligenceTask(t, store, mission.ID, "second")
		first = transitionTask(t, store, first, domain.TaskProvisioning, "concurrent_first")
		if first.State != domain.TaskProvisioning {
			t.Fatalf("first=%+v", first)
		}
		second = transitionTask(t, store, second, domain.TaskProvisioning, "concurrent_second")
		if second.State != domain.TaskNeedsAttention {
			t.Fatalf("second=%+v", second)
		}
	})
	t.Run("task attempts", func(t *testing.T) {
		store, mission := intelligenceMission(t, domain.MissionBudget{MaxTaskAttempts: 1})
		defer store.Close()
		task := intelligenceTask(t, store, mission.ID, "attempt")
		task = transitionTask(t, store, task, domain.TaskFailed, "attempt_fail")
		expired, err := store.RetryTask(context.Background(), "cmd_attempt_retry", RetryTaskInput{
			TaskID: task.ID, ExpectedVersion: task.Version, Actor: "commander",
		})
		if !errors.Is(err, ErrAttemptBudget) || expired.State != domain.TaskNeedsAttention {
			t.Fatalf("expired=%+v err=%v", expired, err)
		}
		assertNeedsAttention(t, store, task.ID)
	})
	t.Run("validation rounds", func(t *testing.T) {
		store, task := readyValidationTask(t)
		defer store.Close()
		if _, err := store.db.Exec(`UPDATE missions SET max_validation_runs = 1 WHERE id = ?`, task.MissionID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.BeginValidation(context.Background(), "cmd_round_one", validation.BeginInput{TaskID: task.ID, Attempt: 1, Actor: "commander"}); err != nil {
			t.Fatal(err)
		}
		expired, err := store.BeginValidation(context.Background(), "cmd_round_two", validation.BeginInput{TaskID: task.ID, Attempt: 1, Actor: "commander"})
		if err != nil || expired.State != domain.TaskNeedsAttention {
			t.Fatalf("expired=%+v err=%v", expired, err)
		}
	})
}

func TestUpdateMissionBudgetUpdatesClearsAndUnblocksRetry(t *testing.T) {
	store, mission := intelligenceMission(t, domain.MissionBudget{MaxWallClock: time.Hour, MaxConcurrentTasks: 1, MaxTaskAttempts: 1, MaxValidationRuns: 1})
	defer store.Close()
	updates := []struct {
		name  string
		apply func(*domain.MissionBudget)
		check func(domain.MissionBudget) bool
	}{
		{"wall clock", func(b *domain.MissionBudget) { b.MaxWallClock = 2 * time.Hour }, func(b domain.MissionBudget) bool { return b.MaxWallClock == 2*time.Hour }},
		{"concurrency", func(b *domain.MissionBudget) { b.MaxConcurrentTasks = 2 }, func(b domain.MissionBudget) bool { return b.MaxConcurrentTasks == 2 }},
		{"attempts", func(b *domain.MissionBudget) { b.MaxTaskAttempts = 2 }, func(b domain.MissionBudget) bool { return b.MaxTaskAttempts == 2 }},
		{"validation", func(b *domain.MissionBudget) { b.MaxValidationRuns = 2 }, func(b domain.MissionBudget) bool { return b.MaxValidationRuns == 2 }},
		{"tokens", func(b *domain.MissionBudget) { value := int64(100); b.MaxTokens = &value }, func(b domain.MissionBudget) bool { return b.MaxTokens != nil && *b.MaxTokens == 100 }},
		{"cost", func(b *domain.MissionBudget) { value := "1.25"; b.MaxCost = &value }, func(b domain.MissionBudget) bool { return b.MaxCost != nil && *b.MaxCost == "1.25" }},
	}
	for _, update := range updates {
		budget := mission.Budget
		update.apply(&budget)
		result, err := store.UpdateMissionBudget(context.Background(), domain.CommandID("cmd_budget_"+strings.ReplaceAll(update.name, " ", "_")), UpdateMissionBudgetInput{MissionID: mission.ID, ExpectedVersion: mission.Version, Budget: budget, Actor: "operator"})
		if err != nil || !update.check(result.Mission.Budget) {
			t.Fatalf("update %s result=%+v err=%v", update.name, result, err)
		}
		mission = result.Mission
	}
	task := intelligenceTask(t, store, mission.ID, "retry after clear")
	task = transitionTask(t, store, task, domain.TaskFailed, "budget_retry_failed")
	budget := mission.Budget
	budget.MaxTaskAttempts = 1
	missionUpdate, err := store.UpdateMissionBudget(context.Background(), "cmd_budget_restore_attempt_cap", UpdateMissionBudgetInput{MissionID: mission.ID, ExpectedVersion: mission.Version, Budget: budget, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	mission = missionUpdate.Mission
	exhausted, err := store.RetryTask(context.Background(), "cmd_budget_retry_exhaust", RetryTaskInput{TaskID: task.ID, ExpectedVersion: task.Version, Actor: "commander"})
	if !errors.Is(err, ErrAttemptBudget) || exhausted.State != domain.TaskNeedsAttention {
		t.Fatalf("exhausted=%+v err=%v", exhausted, err)
	}
	clear := mission.Budget
	clear.MaxWallClock = 0
	clear.MaxConcurrentTasks = 0
	clear.MaxTaskAttempts = 0
	clear.MaxValidationRuns = 0
	clear.MaxTokens = nil
	clear.MaxCost = nil
	input := UpdateMissionBudgetInput{MissionID: mission.ID, ExpectedVersion: mission.Version, Budget: clear, Actor: "operator"}
	updated, err := store.UpdateMissionBudget(context.Background(), "cmd_budget_clear", input)
	if err != nil || updated.Mission.Budget.MaxWallClock != 0 || updated.Mission.Budget.MaxConcurrentTasks != 0 || updated.Mission.Budget.MaxTaskAttempts != 0 || updated.Mission.Budget.MaxValidationRuns != 0 || updated.Mission.Budget.MaxTokens != nil || updated.Mission.Budget.MaxCost != nil || len(updated.RecoverableTaskIDs) != 1 || updated.RecoverableTaskIDs[0] != task.ID {
		t.Fatalf("clear=%+v err=%v", updated, err)
	}
	repeated, err := store.UpdateMissionBudget(context.Background(), "cmd_budget_clear", input)
	if err != nil || repeated.Mission.Version != updated.Mission.Version || len(repeated.RecoverableTaskIDs) != 1 {
		t.Fatalf("idempotent repeat=%+v err=%v", repeated, err)
	}
	if _, err := store.RetryTask(context.Background(), "cmd_budget_retry_legal", RetryTaskInput{TaskID: task.ID, ExpectedVersion: exhausted.Version, Actor: "commander"}); err != nil {
		t.Fatalf("retry after clear: %v", err)
	}
	events, err := store.MissionEvents(context.Background(), mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == "mission.budget.updated" {
			count++
		}
	}
	if count != len(updates)+2 {
		t.Fatalf("budget update events=%d, want %d", count, len(updates)+2)
	}
}

func TestWorkerBudgetDimensionsExpireToNeedsAttention(t *testing.T) {
	for _, test := range []struct {
		name, dimension  string
		budget           domain.WorkerBudget
		firstReservation bool
	}{
		{"runtime", "runtime", domain.WorkerBudget{MaxRuntime: time.Nanosecond, MaxRestarts: 2, MaxFixRounds: 2}, false},
		{"restarts", "restart", domain.WorkerBudget{MaxRuntime: time.Hour, MaxRestarts: 1, MaxFixRounds: 2}, true},
		{"fix rounds", "fix_round", domain.WorkerBudget{MaxRuntime: time.Hour, MaxRestarts: 2, MaxFixRounds: 1}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, task, session := runningWorker(t, test.budget)
			defer store.Close()
			if test.firstReservation {
				if _, err := store.ReserveWorkerBudget(context.Background(), "cmd_worker_first", ReserveWorkerBudgetInput{
					TaskID: task.ID, Attempt: 1, SessionID: session.ID, ExpectedVersion: session.Version,
					Dimension: test.dimension, ObservedAt: session.CreatedAt, Actor: "test",
				}); err != nil {
					t.Fatal(err)
				}
				session, _ = store.WorkerSession(context.Background(), task.ID, 1)
			}
			expired, err := store.ReserveWorkerBudget(context.Background(), "cmd_worker_expire", ReserveWorkerBudgetInput{
				TaskID: task.ID, Attempt: 1, SessionID: session.ID, ExpectedVersion: session.Version,
				Dimension: test.dimension, ObservedAt: session.CreatedAt.Add(time.Nanosecond), Actor: "test",
			})
			if err != nil || expired.State != domain.TaskNeedsAttention {
				t.Fatalf("expired=%+v err=%v", expired, err)
			}
		})
	}
}

func TestCommanderBudgetDimensionsExpireToNeedsAttention(t *testing.T) {
	for _, test := range []struct {
		name    string
		budget  domain.CommanderBudget
		elapsed time.Duration
	}{
		{"turns", domain.CommanderBudget{MaxTurns: 1, MaxDuration: time.Hour}, 0},
		{"duration", domain.CommanderBudget{MaxTurns: 10, MaxDuration: time.Nanosecond}, time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mission := intelligenceMission(t, domain.MissionBudget{})
			defer store.Close()
			session, err := store.RecordCommanderSession(context.Background(), "cmd_commander_record", RecordCommanderSessionInput{
				MissionID: mission.ID, Actor: "operator", Session: domain.CommanderSession{
					ID: "csn_budget", Runtime: "codex", HerdrSessionName: "fm-lab-budget",
					HerdrWorkspaceID: "workspace", HerdrTabID: "tab", HerdrPaneID: "pane",
					HerdrAgentName: "prime", AgentSessionID: "native", Budget: test.budget,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			expired, err := store.ReserveCommanderTurn(context.Background(), "cmd_commander_expire", ReserveCommanderTurnInput{
				MissionID: mission.ID, SessionID: session.ID, ExpectedVersion: session.Version,
				ObservedAt: session.CreatedAt.Add(test.elapsed), Actor: "test",
			})
			if err != nil || expired.State != domain.CommanderSessionNeedsAttention {
				t.Fatalf("expired=%+v err=%v", expired, err)
			}
		})
	}
}

func TestCommanderBudgetRenewalClearsPauseAndRestartsWindow(t *testing.T) {
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	defer store.Close()
	session, err := store.RecordCommanderSession(context.Background(), "cmd_commander_renew_record", RecordCommanderSessionInput{MissionID: mission.ID, Actor: "operator", Session: domain.CommanderSession{ID: "csn_renew", Runtime: "codex", HerdrSessionName: "lab", HerdrWorkspaceID: "workspace", HerdrTabID: "tab", HerdrPaneID: "pane", HerdrAgentName: "prime", AgentSessionID: "native", Budget: domain.CommanderBudget{MaxTurns: 1, MaxDuration: time.Minute}}})
	if err != nil {
		t.Fatal(err)
	}
	exhausted, err := store.ReserveCommanderTurn(context.Background(), "cmd_commander_renew_exhaust", ReserveCommanderTurnInput{MissionID: mission.ID, SessionID: session.ID, ExpectedVersion: session.Version, Actor: "test"})
	if err != nil || exhausted.State != domain.CommanderSessionNeedsAttention {
		t.Fatalf("exhausted=%+v err=%v", exhausted, err)
	}
	renewed, err := store.RenewCommanderBudget(context.Background(), "cmd_commander_renew", RenewCommanderBudgetInput{SessionID: exhausted.ID, ExpectedVersion: exhausted.Version, Budget: exhausted.Budget, Actor: "operator"})
	if err != nil || renewed.State != domain.CommanderSessionRunning || renewed.TurnCount != 0 || renewed.FailureReason != "" {
		t.Fatalf("renewed=%+v err=%v", renewed, err)
	}
	if renewed.BudgetStartedAt.Before(session.BudgetStartedAt) {
		t.Fatalf("budget window did not restart: %s <= %s", renewed.BudgetStartedAt, session.BudgetStartedAt)
	}
	for _, command := range []domain.CommandID{"cmd_commander_budget_signal_one", "cmd_commander_budget_signal_two"} {
		if _, err := store.EnsureCommanderBudgetSignal(context.Background(), command, exhausted.ID); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.Signals(context.Background(), ListSignalsFilter{MissionID: mission.ID, Status: signals.SignalOpen})
	if err != nil || len(items) != 1 || items[0].Question != commanderBudgetQuestion || items[0].Recommendation != "renew" {
		t.Fatalf("budget signals=%+v err=%v", items, err)
	}
	resumed, err := store.ReserveCommanderTurn(context.Background(), "cmd_commander_renew_resume", ReserveCommanderTurnInput{MissionID: mission.ID, SessionID: renewed.ID, ExpectedVersion: renewed.Version, Actor: "test"})
	if err != nil || resumed.State != domain.CommanderSessionRunning || resumed.TurnCount != 1 {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
}

func TestUnsetCommanderBudgetNeverBinds(t *testing.T) {
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	defer store.Close()
	session, err := store.RecordCommanderSession(context.Background(), "cmd_commander_unlimited_record", RecordCommanderSessionInput{MissionID: mission.ID, Actor: "operator", Session: domain.CommanderSession{ID: "csn_unlimited", Runtime: "codex", HerdrSessionName: "lab", HerdrWorkspaceID: "workspace", HerdrTabID: "tab", HerdrPaneID: "pane", HerdrAgentName: "prime", AgentSessionID: "native"}})
	if err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < 31; turn++ {
		session, err = store.ReserveCommanderTurn(context.Background(), domain.CommandID("cmd_commander_unlimited_"+strconv.Itoa(turn)), ReserveCommanderTurnInput{MissionID: mission.ID, SessionID: session.ID, ExpectedVersion: session.Version, ObservedAt: session.BudgetStartedAt.Add(46 * time.Minute), Actor: "test"})
		if err != nil || session.State != domain.CommanderSessionRunning {
			t.Fatalf("turn=%d session=%+v err=%v", turn, session, err)
		}
	}
}

func intelligenceMission(t *testing.T, budget domain.MissionBudget) (*Store, domain.Mission) {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.db"))
	project, err := store.CreateProject(context.Background(), "cmd_project", CreateProjectInput{Name: "project", Path: "/tmp/" + strings.ReplaceAll(t.Name(), "/", "-")})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	mission, err := store.CreateMission(context.Background(), "cmd_mission", CreateMissionInput{ProjectID: project, Title: "mission", Objective: "objective", Budget: budget})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, mission
}

func intelligenceTask(t *testing.T, store *Store, missionID domain.MissionID, title string) domain.Task {
	t.Helper()
	task, err := store.CreateTask(context.Background(), domain.CommandID("cmd_task_"+title), CreateTaskInput{MissionID: missionID, Kind: domain.TaskImplementation, Title: title, Objective: "objective", WorkerAgent: "codex", DeliveryMode: domain.DeliveryGate})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func transitionTask(t *testing.T, store *Store, task domain.Task, to domain.TaskState, suffix string) domain.Task {
	t.Helper()
	updated, err := store.TransitionTask(context.Background(), domain.CommandID("cmd_"+suffix), TransitionTaskInput{TaskID: task.ID, Attempt: task.CurrentAttempt, ExpectedState: task.State, ExpectedVersion: task.Version, To: to, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func runningWorker(t *testing.T, budget domain.WorkerBudget) (*Store, domain.Task, domain.WorkerSession) {
	t.Helper()
	store, mission := intelligenceMission(t, domain.MissionBudget{})
	task := intelligenceTask(t, store, mission.ID, "worker")
	task = transitionTask(t, store, task, domain.TaskProvisioning, "worker_provision")
	if _, err := store.RecordTreehouseLease(context.Background(), "cmd_worker_lease", RecordTreehouseLeaseInput{TaskID: task.ID, Attempt: 1, ExpectedVersion: task.Version, Actor: "test", Lease: domain.TreehouseLease{LeaseID: "lease", LeaseHolder: "holder", WorktreePath: "/tmp/worktree", Project: "project", Branch: "branch", BaseSHA: "base"}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, _ = store.Task(context.Background(), task.ID)
	task = transitionTask(t, store, task, domain.TaskStarting, "worker_starting")
	session, err := store.RecordWorkerSession(context.Background(), "cmd_worker_record", RecordWorkerSessionInput{TaskID: task.ID, Attempt: 1, Actor: "scheduler", Session: domain.WorkerSession{ID: "wsn_budget", Runtime: "codex", HerdrSessionName: "fm-lab-budget", HerdrWorkspaceID: "workspace", HerdrTabID: "tab", HerdrPaneID: "pane", HerdrAgentName: "worker", AgentSessionID: "native", Budget: budget}})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, _ = store.Task(context.Background(), task.ID)
	return store, task, session
}

func assertNeedsAttention(t *testing.T, store *Store, taskID domain.TaskID) {
	t.Helper()
	task, err := store.Task(context.Background(), taskID)
	if err != nil || task.State != domain.TaskNeedsAttention {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}
