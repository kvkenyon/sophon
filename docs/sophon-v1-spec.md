# Sophon
## Version 1 Product & Engineering Specification

**Status:** Build specification  
**Product:** Sophon<br>
**Commander runtimes:** Pi, Claude Code, Codex<br>
**Workers:** Pi, Claude Code, Codex  
**Terminal runtime:** Herdr  
**Worktree runtime:** Treehouse  
**Validation / delivery:** no-mistakes  
**Control plane:** Go + SQLite  
**Deployment:** Local-first, single machine

---

# 1. Product definition

Sophon is a local control plane for autonomous software engineering.

A human interacts with one persistent **commander agent**. The commander understands objectives, decomposes work, delegates tasks to specialized workers, monitors progress, responds to findings, and synthesizes results.

Sophon sits underneath the commander and makes multi-agent work reliable.

It owns:

- missions;
- tasks;
- dependencies;
- worker identity;
- worker lifecycle;
- Treehouse leases;
- task attempts;
- state transitions;
- blockers and decisions;
- completion evidence;
- validation results;
- delivery records;
- crash recovery;
- durable history.

Agents provide intelligence.

Sophon provides authority and state.

The core principle is:

> **Reasoning may be probabilistic. Execution state may not be.**

---

# 2. Product goal

Sophon should provide the useful behavior of FirstMate while replacing its brittle shell, polling, file-state, and backend machinery with a small deterministic control plane.

The desired user experience is:

```text
Human
  ↓
Commander
  ↓
"Fix this bug, investigate that slowdown,
and clean up these tests."
  ↓
Sophon
  ├── Codex working on bug
  ├── Claude investigating slowdown
  └── Pi improving tests
  ↓
validated PRs + reports + decisions
```

The human should not have to manually:

- create worktrees;
- open terminal windows;
- monitor agents;
- remember which worker owns which branch;
- re-explain context after a worker becomes idle;
- determine whether an agent really completed;
- run the validation pipeline;
- recover tasks after the orchestrator restarts.

---

# 3. Design foundations

Sophon combines four existing systems and a runtime-adapter boundary rather than reimplementing them.

## FirstMate

Use FirstMate as the behavioral upstream.

Reuse and adapt:

- core delegation instructions;
- coding guidelines;
- diagnostic reasoning;
- human-authority boundaries;
- unresolved-decision handling;
- status reporting;
- worker recovery;
- project-management behavior;
- supervision patterns.

Replace:

- shell orchestration;
- watcher architecture;
- task metadata files;
- status logs as state;
- tmux/backend abstractions;
- manual polling infrastructure.

FirstMate already contains a large behavioral contract and modular skills around these responsibilities.  

## Commander runtimes

Pi, Claude Code, and Codex are co-equal commander runtimes. Sophon supplies each with a persistent, Herdr-managed session and a structured CLI/API surface for missions, tasks, signals, and recovery.

Useful concepts adopted by Sophon include:

- persistent commander sessions;
- programmatic orchestration over structured state;
- explicit autonomous budgets;
- addressable sessions;
- event-driven wakeups;
- durable prompts and memory.

## Herdr

Herdr owns runtime processes and visible terminals.

Sophon should never recreate a terminal multiplexer.

## Treehouse

Treehouse owns Git worktree allocation and lease identity.

Sophon never performs unmanaged `git worktree` operations.

## no-mistakes

no-mistakes owns the final code-quality and delivery gate.

Sophon coordinates it rather than rebuilding its review loop.

---

# 4. Architecture boundaries

Each system owns one topology.

```text
Commander runtime
    │
    │ reasoning topology
    ▼
Sophon
    │
    │ execution topology
    ▼
Herdr
    │
    │ process topology
    ▼
Treehouse
    │
    │ repository isolation
    ▼
no-mistakes
    │
    │ validation / delivery
    ▼
GitHub
```

More concretely:

```text
Human
  │
  ▼
Commander
Pi / Claude Code / Codex
  │
  ▼
sophond
  ├── mission state
  ├── task state
  ├── scheduler
  ├── SQLite
  ├── signals
  ├── worker sessions
  ├── validation cache
  ├── event timeline
  ├── recovery
  │
  ├── commander runtime adapter
  ├── Herdr
  ├── Treehouse
  ├── Git
  └── no-mistakes
```

---

# 5. Terminology

Use straightforward terminology.

| Term | Definition |
|---|---|
| **Operator** | Human user |
| **Commander** | Main coordinating agent |
| **Mission** | High-level operator objective |
| **Task** | One independently schedulable unit of work |
| **Worker** | Agent assigned to a task |
| **Worker session** | Persistent agent identity attached to a task |
| **Attempt** | One execution attempt for a task |
| **Signal** | Durable question/blocker requiring attention |
| **Lease** | Treehouse ownership record |
| **Artifact** | Report, validation result, diff summary, etc. |
| **Delivery** | Branch or PR creation process |

Do not expose elaborate role-playing terminology throughout the product.

---

# 6. V1 scope

Version 1 supports:

- local single-user operation;
- one persistent commander;
- Pi, Claude Code, and Codex as co-equal commanders;
- Pi, Claude Code, Codex workers;
- multiple projects;
- multiple missions;
- parallel workers;
- persistent worker sessions;
- implementation tasks;
- scout tasks;
- review tasks;
- task dependencies;
- exclusive resource locks;
- Treehouse worktree leases;
- Herdr terminal sessions;
- structured completion;
- structured blockers;
- task retries;
- cached validation;
- no-mistakes delivery;
- direct PR delivery;
- branch-only delivery;
- durable mission history;
- restart recovery;
- operator decisions;
- mission summaries;
- FirstMate-derived skills;
- execution budgets.

---

# 7. Explicit non-goals

V1 will not support:

- automatic merging;
- production deployment;
- multi-machine scheduling;
- hosted infrastructure;
- organization/team accounts;
- Discord/Slack/X relays;
- arbitrary terminal runtimes;
- secondmate hierarchy;
- remote worker machines;
- workers sharing a writable worktree;
- autonomous self-modification of control-plane rules;
- learned security or delivery policy;
- unconstrained recursive coding agents;
- generic plugin marketplace;
- automatic merge-conflict resolution.

---

# 8. Core invariant

There are two different state machines:

```text
Task State
```

and:

```text
Worker Session State
```

They must never be conflated.

A worker may be idle while its task remains active.

A worker may be inactive while its task waits for code review.

A task may be completed even though its worker process has been unloaded.

Example:

```text
Task:
ready

Worker session:
inactive
```

This is valid.

The following implication is forbidden:

```text
worker idle
    ⇒
task done
```

---

# 9. Mission model

A mission is the durable representation of the operator's goal.

Example:

```text
Improve invitation acceptance reliability while preserving
existing external API behavior.
```

A mission contains:

```go
type Mission struct {
    ID                 MissionID
    ProjectID          ProjectID
    CommanderSessionID SessionID

    Title              string
    Objective          string
    AcceptanceCriteria []Criterion

    State              MissionState
    Version            int64

    Budget             MissionBudget

    CreatedAt          time.Time
    CompletedAt        *time.Time
}
```

Mission budget:

```go
type MissionBudget struct {
    MaxWallClock       time.Duration
    MaxConcurrentTasks int
    MaxTaskAttempts    int
    MaxValidationRuns  int

    // optional when provider accounting is available
    MaxTokens          *int64
    MaxCost            *decimal.Decimal
}
```

---

# 10. Mission completion

The commander may recommend that a mission is complete.

The commander does not authoritatively complete it.

Mission completion requires:

```text
all required tasks terminal-success
AND
no unresolved blocking signals
AND
mission acceptance criteria evaluated
AND
completion policy satisfied
```

Only then:

```text
active
  ↓
completing
  ↓
completed
```

The commander produces a final semantic summary after deterministic completion eligibility has been established.

---

# 11. Task types

## Implementation

Produces repository changes.

Required:

- assigned Treehouse lease;
- committed changes;
- clean worktree;
- new commit after base SHA;
- structured completion result;
- required validation.

## Scout

Read-only investigation.

Produces:

```text
report.md
```

with:

- summary;
- evidence;
- findings;
- likely cause;
- recommendations;
- uncertainty;
- unresolved decisions.

Scout completion does not require a Git commit.

## Review

Inspects committed work.

Examples:

- architectural review;
- security review;
- test review;
- regression analysis.

A review worker must not share another worker's writable worktree.

---

# 12. Persistent worker model

A major v1 feature is:

> **A worker represents a durable specialist assigned to a task, not one model invocation.**

Lifecycle:

```text
task created
    ↓
worker session created
    ↓
implementation
    ↓
idle
    ↓
review feedback arrives
    ↓
same worker wakes
    ↓
fix
    ↓
validation
    ↓
CI failure
    ↓
same worker wakes
    ↓
fix
    ↓
delivery complete
    ↓
worker may become inactive/delete
```

This avoids repeatedly giving fresh agents the same context.

---

# 13. Worker session states

```text
starting
   ↓
running
   ↓
idle
   ↓ inactivity
inactive
   ↓ wake
running
```

Additional states:

```text
lost
failed
stopping
stopped
```

Definitions:

### `running`

Agent is actively processing.

### `idle`

Agent remains alive and addressable but is waiting.

### `inactive`

Session is persisted but its active process/resources may be released.

### `lost`

Expected runtime session cannot be found and reconciliation is required.

---

# 14. Task state machine

```text
queued
  ↓
provisioning
  ↓
starting
  ↓
running
  ↓
collecting
  ↓
ready
  ↓
validating
  ↓
delivered
```

Alternative paths:

```text
running
  ↔
blocked
```

```text
validating
  ↔
delivery_blocked
```

```text
any nonterminal
  → needs_attention
```

```text
any nonterminal
  → cancelling
  → cancelled
```

Cancellation is terminal even when cleanup is imperfect. The control plane
first records `cancelling` then `cancelled`; it conditionally returns the
attempt's Treehouse lease by lease ID and holder, and fences/audits an
ambiguous return. A live task-owned Herdr tab is then closed and its worker
session recorded stopped; a missing or already-dead session is tolerated.

```text
any nonterminal
  → failed
```

Branch-only completion:

```text
ready
  → delivered_branch
```

Scout:

```text
queued
→ provisioning
→ starting
→ running
→ collecting
→ report_ready
```

---

# 15. Task attempts

Retrying a task does not mutate the identity of the existing execution.

Instead:

```text
Task T
├── attempt 1
├── attempt 2
└── attempt 3
```

Each attempt receives:

- attempt number;
- its own Treehouse lease;
- worker session;
- base SHA;
- runtime metadata;
- completion record.

A stale attempt can never complete a newer attempt.

Worker completion commands include:

```text
task_id
attempt
```

---

# 16. Treehouse integration

Acquire:

```bash
treehouse get \
  --lease \
  --lease-holder "sophon:<task>:<attempt>" \
  --json
```

Persist:

```text
lease_id
lease_holder
worktree_path
project
branch
base_sha
```

Release only conditionally:

```bash
treehouse return \
  --force \
  --if-lease-id "$LEASE_ID" \
  --if-lease-holder "$LEASE_HOLDER" \
  "$WORKTREE"
```

Never release by path alone.

Never silently replace a missing or mismatched lease.

---

# 17. Herdr integration

Herdr owns:

- PTYs;
- processes;
- workspaces;
- tabs;
- panes;
- agent startup;
- visible interaction;
- session persistence.

Recommended layout:

```text
Sophon
├── Commander
│   └── Pi / Claude Code / Codex
│
├── hifive
│   ├── invitation-race
│   │   └── Codex
│   ├── contact-import-profile
│   │   └── Claude
│   └── regression-review
│       └── Pi
```

Sophon stores stable Herdr identifiers.

Tab names are presentation only.

They are not identity.

---

# 18. Commander runtimes

Supported:

```text
pi
claude
codex
```

All commander runtimes use a terminal-driven adapter through Herdr, just like workers. The adapter provides prompting, steering, follow-ups, cancellation, state inspection, event wakeups, and session persistence without relying on runtime-specific RPC.

---

# 19. Commander session architecture

Sophon creates each commander as a Herdr-managed terminal session and persists its runtime, Herdr placement, and resumable session identity. A runtime adapter translates the common commander operations to Pi, Claude Code, or Codex.

Conceptual interface:

```go
type Commander interface {
    Start(context.Context, CommanderConfig) (*Session, error)

    Prompt(context.Context, SessionID, Message) error
    Steer(context.Context, SessionID, Message) error
    FollowUp(context.Context, SessionID, Message) error

    State(context.Context, SessionID) (CommanderState, error)
    Abort(context.Context, SessionID) error

    Events(context.Context, SessionID) (<-chan CommanderEvent, error)
}
```

---

# 20. Commander control surface

The `sophon` CLI and local API are the common control surface for every commander runtime. Prompts teach commanders to query structured mission and task state, create and update work through command-idempotent operations, message workers, resolve signals, retry or cancel tasks, and request delivery.

Authoritative logic remains in Go. A future runtime may be added behind the commander adapter interface when it satisfies the same lifecycle and conformance requirements.

---

# 21. Commander reasoning boundaries

Commanders may use their own reasoning tools to analyze plans, compare worker reports, inspect artifacts, critique architecture, and identify missing tests. They may not acquire Treehouse leases, alter mission or task state outside the control surface, modify registered repositories, push code, or deliver PRs. Repository-changing work always becomes a Sophon task.

---

# 22. Programmatic commander philosophy

The commander should operate on structured state rather than repeatedly interpreting prose.

Good:

```text
sophon task list --mission <id> --state blocked
```

Bad:

```text
Read five terminal windows and infer which one is blocked.
```

The CLI/API lets any commander calculate over structured mission state while keeping model context small.

---

# 23. FirstMate behavioral reuse

Do not rewrite FirstMate's behavioral prompt set from scratch.

Vendor the relevant upstream content.

```text
prompts/
├── upstream/
│   └── firstmate/
│       ├── VERSION
│       ├── AGENTS.md
│       └── skills/
│
├── commander/
├── workers/
└── skills/
```

Store upstream repository + commit.

---

# 24. Role migration

FirstMate's roles:

```text
human = captain
coordinator = firstmate
executor = crewmate
```

Sophon:

```text
human = operator
coordinator = commander
executor = worker
```

Mapping:

| FirstMate | Sophon |
|---|---|
| Captain | Operator |
| FirstMate | Commander |
| Crewmate | Worker |
| Fleet | Mission/workers |
| Captain decision | Operator signal |
| SecondMate | Removed from v1 |

This requires semantic migration, not search-and-replace.

---

# 25. FirstMate skills to port

## Port in V1

```text
ahoy
bearings
ask-user-authority
decision-hold-lifecycle
diagnostic-reasoning
firstmate-coding-guidelines
project-management
stuck-crewmate-recovery
bootstrap-diagnostics
harness-adapters
```

Rename conceptually:

```text
ahoy
→ recap

bearings
→ status

ask-user-authority
→ operator-authority

decision-hold-lifecycle
→ decision-lifecycle

firstmate-coding-guidelines
→ coding-guidelines

stuck-crewmate-recovery
→ worker-recovery

harness-adapters
→ agent-adapters
```

## Do not port in V1

```text
secondmate-provisioning
fmx-respond
orca backend
Relay/X integrations
remote secondmates
multiple shell backends
legacy watchers
AFK subsystem
```

---

# 26. Behavior vs mechanism classification

Every imported FirstMate rule must be classified.

## Deterministic invariant

Implement in Go.

Examples:

- lease ownership;
- state transition legality;
- merge disabled;
- SHA verification;
- task attempt fencing;
- delivery idempotency.

## Behavioral policy

Keep in agent instructions.

Examples:

- when to delegate;
- how to decompose;
- what constitutes material scope expansion;
- how to summarize evidence;
- when to recommend escalation.

## Both

Enforce mechanically and explain behaviorally.

Examples:

- commander does not edit projects;
- worker does not release worktree;
- unresolved decisions become signals;
- task completion must be structured;
- worker cannot contact operator directly.

---

# 27. Worker prompts

Worker receives:

```text
common worker prompt
+
task-kind prompt
+
runtime overlay
+
generated task brief
```

Common rules:

```text
You are a Sophon worker.

You own one assigned task.

Work only inside the assigned worktree.

Do not:
- create/delete worktrees;
- push or merge without the delivery system;
- change Sophon task state directly;
- contact the operator directly;
- expand product scope silently;
- destroy existing work;
- declare success without structured completion.

When blocked, report a structured blocker.

When complete:
1. validate;
2. commit;
3. ensure the tree is clean;
4. report structured completion.
```

---

# 28. Task brief

Generated path:

```text
~/.sophon/tasks/<task>/<attempt>/brief.md
```

Contains:

- mission;
- task;
- attempt;
- project;
- worktree;
- branch;
- base SHA;
- objective;
- acceptance criteria;
- relevant dependency results;
- validation requirements;
- delivery mode;
- permissions;
- forbidden actions;
- completion instructions.

---

# 29. Structured worker completion

Implementation worker:

```bash
sophon worker complete TASK \
  --attempt 1 \
  --head-sha "$(git rev-parse HEAD)" \
  --result .sophon-result.json
```

Result:

```json
{
  "version": 1,
  "status": "completed",

  "summary": "Fixed concurrent invitation consumption.",

  "verification": [
    {
      "command": "pnpm test invitations",
      "exit_code": 0
    }
  ],

  "changed_files": [
    "src/invitations/accept.ts",
    "src/invitations/accept.test.ts"
  ],

  "risks": []
}
```

Control plane verifies independently:

```text
attempt valid
lease valid
HEAD matches
commit descends from base
new commit exists
tree clean
result valid
```

---

# 30. Structured blockers

```bash
sophon worker block TASK \
  --attempt 1 \
  --kind decision \
  --message blocker.md
```

Kinds:

```text
decision
credential
permission
missing-context
environment
external-dependency
conflict
unsafe-operation
```

Important blockers become Signals.

---

# 31. Signals

A Signal is the durable representation of an unresolved question.

```go
type Signal struct {
    ID             SignalID
    MissionID      MissionID
    TaskID         *TaskID

    Kind           SignalKind

    Question       string
    Context        string
    Options        []Option
    Recommendation string

    Status         SignalStatus
    Answer         *string
}
```

A decision discovered inside:

- a scout report;
- review;
- no-mistakes;
- worker blocker;

must become a Signal before the originating activity is considered completely handled.

This preserves the key behavior of FirstMate's durable decision lifecycle. 

---

# 32. Operator authority

Adapt FirstMate's authority rules.

Commander may autonomously make implementation choices required to satisfy the already accepted contract.

Commander must escalate when a choice:

- materially expands product behavior;
- adds a new guarantee;
- changes compatibility;
- introduces a new threat model;
- adds substantial architectural scope;
- performs destructive action;
- requires security-sensitive authority.

A reviewer saying something is "required" does not itself expand commander authority.

FirstMate already encodes this useful distinction between satisfying accepted intent and expanding the contract. 

---

# 33. Cached validation

Validation should be content-addressed.

Do not rerun expensive validation when nothing relevant changed.

Validation key:

```text
task
head SHA
dirty tree fingerprint
validator version
validation config hash
command hash
environment fingerprint
```

Schema:

```text
validation_runs

id
task_id
attempt
head_sha
workspace_hash
validator
validator_version
config_hash
environment_hash
status
artifact_id
created_at
```

Before running:

```go
result := validationCache.Lookup(key)

if result != nil {
    return result
}
```

Use for:

- unit tests;
- typecheck;
- lint;
- project validation;
- no-mistakes stages when safe.

This is especially useful during repeated review/fix cycles.

---

# 34. Persistent worker reuse during delivery

Example:

```text
Codex implements
    ↓
task ready
    ↓
no-mistakes finds issue
    ↓
Signal? no
    ↓
wake same Codex worker
    ↓
Codex fixes
    ↓
new completion SHA
    ↓
revalidate
```

Do not spawn a fresh worker unless:

- original session is unavailable;
- task is explicitly retried;
- commander intentionally requests another specialist.

---

# 35. Forgotten completion recovery

If Herdr reports worker idle but Sophon lacks a structured outcome:

1. wait stabilization delay;
2. check for completion/failure/blocker;
3. send one recovery prompt;
4. wait;
5. mark `needs_attention` if unresolved.

Never infer success from:

```text
"done"
"all fixed"
"tests pass"
```

inside terminal text.

---

# 36. Execution budgets

Everything autonomous should be bounded.

Mission defaults:

```toml
[mission]
max_duration = "4h"
max_concurrent_tasks = 8
max_task_attempts = 3
max_validation_rounds = 5
```

Worker:

```toml
[worker]
max_runtime = "90m"
max_restarts = 2
max_fix_rounds = 5
```

Commander:

```toml
[commander.autonomous]
max_turns = 30
max_duration = "45m"
```

When a budget expires:

```text
needs_attention
```

Do not let an agent retry indefinitely.

---

# 37. Append-only event timeline

SQLite stores every significant transition.

Examples:

```text
mission.created
task.created
task.queued
lease.acquired
worker.started
worker.running
worker.idle
worker.blocked
signal.created
signal.resolved
worker.resumed
worker.completed
completion.verified
validation.started
validation.failed
worker.resumed
worker.completed
validation.passed
delivery.started
delivery.pr_created
task.delivered
mission.completed
```

Mutable tables are current projections.

Events are durable history.

CLI:

```bash
sophon task timeline <id>
sophon mission timeline <id>
```

---

# 38. Event record

```go
type Event struct {
    Sequence   int64

    MissionID  *MissionID
    TaskID     *TaskID

    Actor      string
    Type       string

    CommandID  *CommandID

    Payload    json.RawMessage

    CreatedAt  time.Time
}
```

Events are append-only.

Never rewrite historical events.

---

# 39. Mission digest

Raw history should not have to fit in commander context.

Each mission therefore has:

```text
raw events        authoritative
artifacts         authoritative
current state     deterministic projection
mission digest    regenerable semantic artifact
```

Example:

```markdown
## Objective

Improve invitation reliability.

## Decisions

- Preserve current API response format.
- Use optimistic concurrency.

## Completed

- Added concurrency regression test.
- Fixed duplicate consumption race.

## Remaining

- Import slowdown investigation active.

## Open Signals

None.
```

The digest may be regenerated when:

- major task completes;
- signal resolves;
- mission context becomes large.

It is not authoritative state.

It is a compact semantic index.

---

# 40. Project knowledge

V1 should include a durable knowledge model, but automated promotion should be conservative.

Knowledge scopes:

```text
immutable-policy
project
learned
mission
```

## Immutable policy

Never agent-editable.

Includes:

```text
lease policy
delivery policy
state machine
merge authority
security rules
completion requirements
```

## Project knowledge

Examples:

```text
Uses pnpm.

Integration tests require Docker.

Generated schema files should not be directly edited.
```

## Learned patterns

Initially proposed by agents but requiring manual or deterministic promotion.

Example:

```text
Invitation integration tests require resetting
the local Redis fixture after crash recovery.
```

## Mission scratch knowledge

Temporary mission-specific information.

---

# 41. Knowledge provenance

Schema:

```text
knowledge

id
project_id
mission_id
scope
kind
content
created_by
trigger_task_id
evidence_artifact_id
confidence
status
created_at
superseded_by
```

Possible statuses:

```text
candidate
active
rejected
superseded
```

V1:

```text
agent may propose
operator/commander may promote where policy allows
```

No autonomous mutation of critical policy.

---

# 42. Self-improvement boundary

Sophon must never learn to weaken its own authority rules.

The following cannot be modified through learned behavior:

- task state machine;
- worktree lease policy;
- destructive-operation policy;
- credentials policy;
- merge policy;
- PR delivery policy;
- security boundaries;
- completion requirements;
- operator authority.

Agent self-improvement applies only above this deterministic boundary.

---

# 43. Family-scoped messaging

Add messaging to the v1 data model.

Initial production use may remain commander-mediated.

Schema should support:

```text
commander ↔ worker
worker ↔ sibling worker
```

Only when:

```text
same mission
AND explicit target
AND permitted relationship
```

No global agent broadcast.

Every message becomes an event.

Conceptual API:

```python
await sophon.message_worker(
    task_id,
    "Review identified a missing transaction retry case."
)
```

Future:

```python
await sophon.message_task(
    from_task=a,
    to_task=b,
    message="Are you changing InvitationStatus names?"
)
```

---

# 44. Future task forks

Design task schema so competing approaches are possible later.

```text
base task
   ├── approach A / Codex
   └── approach B / Claude
```

Fields:

```text
parent_task_id
base_task_id
base_sha
```

V1 need not expose automatic competing implementations.

The schema should not prevent them.

---

# 45. Scheduling

Task starts when:

```text
state == queued
AND dependencies successful
AND project capacity available
AND global capacity available
AND exclusive resources available
AND runtime healthy
AND Treehouse lease can be acquired
```

Defaults:

```toml
[workers]
global_max = 8
per_project_max = 4
```

Tasks may declare resources:

```json
{
  "resources": [
    "database-schema",
    "integration-tests"
  ]
}
```

Only one active task holds an exclusive resource.

---

# 46. Delivery modes

## Gate

Preferred.

```text
ready
→ validating
→ no-mistakes
→ PR
→ CI
→ delivered
```

## PR

```text
ready
→ verify SHA
→ push
→ create PR
→ verify remote SHA
→ delivered
```

## Branch

```text
ready
→ delivered_branch
```

Lease remains retained.

## Merge

Not available in V1.

---

# 47. Delivery idempotency

Every mutation command receives a command ID.

```text
cmd_01K...
```

Repeated command:

```text
same command ID
→ same result
```

PR creation also reconciles externally.

If crash occurs after GitHub creates a PR but before SQLite records it:

```text
find PR by repo + branch + head SHA
```

and record existing PR.

Do not create a duplicate.

---

# 48. SQLite model

Core tables:

```text
projects
commander_sessions
missions
tasks
task_attempts
task_dependencies
worker_sessions
treehouse_leases
resource_locks
signals
validation_runs
artifacts
knowledge
messages
events
commands
deliveries
```

---

# 49. Important task fields

```text
tasks

id
mission_id
parent_task_id
base_task_id

kind
title
objective

state
version
priority

worker_agent
delivery_mode

current_attempt

created_at
updated_at
completed_at
```

Attempts:

```text
task_attempts

task_id
attempt

base_sha
head_sha
branch

worktree_path

treehouse_lease_id
treehouse_lease_holder

worker_session_id

created_at
started_at
completed_at
```

---

# 50. Compare-and-swap state transitions

All meaningful state changes are conditional.

```sql
UPDATE tasks
SET
  state = ?,
  version = version + 1
WHERE
  id = ?
  AND state = ?
  AND version = ?;
```

If zero rows update:

```text
reload
```

Do not blindly retry a stale transition.

---

# 51. Recovery

On startup:

```text
open SQLite
run migrations
connect Herdr
reconnect commander session
inspect Treehouse
inspect active tasks
reconcile external state
resume scheduler
```

For each nonterminal task:

### Lease valid, worker available

Resume observation.

### Lease valid, worker inactive

Keep task state.

Reactivate worker only if needed.

### Lease valid, worker missing

```text
needs_attention
```

### Lease mismatch

Fence task.

```text
failed
```

Never touch new lease holder.

### Worktree missing

Fail attempt.

Retry requires a new attempt.

### PR exists but isn't recorded

Reconcile PR.

### no-mistakes partially complete

Resume from independently verified external state.

---

# 52. CLI

Binary:

```text
sophon
```

Daemon:

```text
sophond
```

Local state defaults to `~/.sophon`. For compatibility, when that directory is absent and `~/.parallel-intellect` exists, all state remains in the legacy directory and retains its legacy database and daemon filenames. The one-step migration is:

```bash
mv ~/.parallel-intellect ~/.sophon
```

Setup:

```bash
sophon init
sophon doctor
```

Projects:

```bash
sophon project add .
sophon project list
sophon project inspect hifive
```

Commander:

```bash
sophon commander start --agent codex
sophon commander attach
sophon commander status
```

Missions:

```bash
sophon mission list
sophon mission inspect <id>
sophon mission timeline <id>
sophon mission cancel <id>
```

Tasks:

```bash
sophon task list
sophon task inspect <id>
sophon task timeline <id>

sophon task retry <id>
sophon task cancel <id>

sophon task prompt <id> --file message.md
sophon task deliver <id>
sophon task release <id>
```

Signals:

```bash
sophon signal list
sophon signal inspect <id>
sophon signal resolve <id>
```

All commands support:

```text
--json
```

where applicable.

---

# 53. FirstMate-derived commands

Provide commander skills equivalent to:

```text
/status
/recap
```

## `/status`

Live deterministic mission snapshot.

Equivalent behavioral role to FirstMate Bearings.

Sections:

```text
Needs Your Attention
Recently Completed
Underway
Up Next
```

Current state comes from Sophon, never chat history. FirstMate's Bearings similarly distinguishes action-required, completed, active, and queued work using structured state.

## `/recap`

Conversation/session-only recap.

Do not gather fresh task state.

This preserves the useful distinction FirstMate makes between `/ahoy` and `/bearings`. 

---

# 54. Observability

Structured logs.

Common fields:

```text
mission_id
task_id
attempt
command_id
worker_session_id
herdr_pane_id
lease_id
branch
base_sha
head_sha
```

Metrics:

```text
active tasks
idle workers
inactive workers
blocked tasks
queue depth
task duration
validation cache hit rate
validation failures
worker restarts
task retries
forgotten completions
lease mismatches
signals opened
signals resolved
delivery duration
```

---

# 55. Security model

Sophon is a coordinator, not a sandbox.

Workers and commanders can execute local code under user permissions.

Protections:

- worktree isolation;
- no commander repository mutation;
- structured task scope;
- deterministic delivery gates;
- merge disabled;
- lease fencing;
- no silent cleanup;
- no critical-policy self-modification;
- credentials excluded from prompts/events where possible.

Untrusted code should eventually run in an external sandbox.

---

# 56. Reliability invariants

V1 is not complete until these hold:

1. One task attempt has at most one active Treehouse lease.
2. A lease is never released by path alone.
3. Old attempts cannot complete current attempts.
4. Worker idle does not imply task completion.
5. Terminal prose cannot complete a task.
6. Delivery only targets verified head SHA.
7. Duplicate delivery requests cannot create duplicate PRs.
8. Committed state survives daemon crashes.
9. Active work is never silently destroyed.
10. A missing worker is not silently replaced while its lease exists.
11. Commander cannot directly mutate internal task state.
12. Every significant state mutation is auditable.
13. Every unresolved operator decision has durable identity.
14. Agent self-improvement cannot weaken control-plane policy.
15. No worker edits a registered project outside its lease.
16. Validation results are tied to immutable input fingerprints.
17. Session lifecycle and task lifecycle remain separate.

---

# 57. Testing

## Unit

Test:

- state machine;
- CAS;
- retries;
- dependency graph;
- scheduler;
- resource locking;
- lease fencing;
- SHA validation;
- result validation;
- validation caching;
- delivery idempotency;
- signals;
- budgets.

## Herdr contract

Test:

```text
start Pi
start Claude
start Codex
prompt
idle
resume
cancel
lost process
Herdr restart
```

## Commander adapter

Test:

```text
start Pi
start Claude
start Codex
prompt
events
steer
follow_up
abort
state
resume
malformed message
Herdr process restart
```

## Treehouse

Test:

```text
acquire
persist
reacquire
stale release
lease mismatch
```

## no-mistakes

Test:

```text
pass
failure
worker fix
operator decision
restart
PR created
```

---

# 58. Crash injection

Kill `sophond`:

```text
before lease
after lease before DB record
after DB record
during worker startup
during running task
after completion callback
during SHA verification
during validation
during no-mistakes
after push
after PR creation
during release
```

Each operation must produce either:

```text
exactly-once result
```

or:

```text
explicit recoverable state
```

Never silent ambiguity.

---

# 59. Real-agent compatibility

Periodic matrix:

| Commander | Worker |
|---|---|
| Pi | Pi |
| Pi | Claude |
| Pi | Codex |
| Claude | Pi |
| Claude | Claude |
| Claude | Codex |
| Codex | Pi |
| Codex | Claude |
| Codex | Codex |

All listed commander/worker combinations are supported.

---

# 60. Repository layout

```text
sophon/
├── cmd/
│   ├── sophon/
│   └── sophond/
│
├── internal/
│   ├── commander/
│   │   ├── pi/
│   │   ├── claude/
│   │   └── codex/
│   │
│   ├── db/
│   ├── delivery/
│   ├── events/
│   ├── git/
│   ├── herdr/
│   ├── knowledge/
│   ├── mission/
│   ├── scheduler/
│   ├── signals/
│   ├── task/
│   ├── treehouse/
│   ├── validation/
│   └── workers/
│
├── prompts/
│   ├── upstream/
│   │   └── firstmate/
│   │
│   ├── commander/
│   ├── workers/
│   └── skills/
│
├── migrations/
├── testdata/
└── third_party/
```

---

# 61. Implementation order

## Milestone 0 — Import behavioral baseline

Before building orchestration:

1. vendor FirstMate prompts;
2. record upstream commit;
3. retain license;
4. map roles;
5. classify rules as:
   - invariant;
   - behavioral policy;
   - both.

Do not optimize prompts yet.

---

## Milestone 1 — Core state machine

Build:

```text
Go CLI
Go daemon
SQLite
projects
missions
tasks
attempts
events
commands
CAS transitions
```

No agents yet.

Prove state transitions with tests.

---

## Milestone 2 — Treehouse

Build:

```text
lease acquisition
lease persistence
lease reconciliation
conditional release
attempt fencing
Git SHA verification
```

This is the first major reliability boundary.

---

## Milestone 3 — One Codex worker

Build the smallest vertical slice:

```text
mission
→ task
→ Treehouse
→ Herdr
→ Codex
→ structured completion
→ SHA verification
→ ready
```

Nothing else until this is deterministic.

---

## Milestone 4 — Persistent workers

Add:

```text
worker_sessions
running/idle/inactive/lost
wake same worker
forgotten-completion recovery
```

---

## Milestone 5 — Claude + Pi

Implement runtime adapters and conformance tests.

---

## Milestone 6 — Validation

Add:

```text
validation pipeline
content-addressed cache
validation artifacts
fix/wake loop
```

---

## Milestone 7 — Commander runtimes

Add:

```text
commander adapter interface
Pi, Claude Code, and Codex commander sessions via Herdr
commander session persistence
event wakeups
steering
follow-ups
```

Mission-decomposition behavior is carried by the commander prompt set rather than a runtime-specific RPC.

---

## Milestone 8 — FirstMate-derived commander skills

Port:

```text
status
recap
operator-authority
decision-lifecycle
diagnostic-reasoning
coding-guidelines
worker-recovery
project-management
```

Mechanisms call structured Sophon APIs.

---

## Milestone 9 — Signals

Add:

```text
durable decisions
operator answers
task dependencies on signals
no-mistakes decision bridge
```

---

## Milestone 10 — Delivery

Add:

```text
branch mode
direct PR
no-mistakes
idempotent PR creation
reconciliation
```

---

## Milestone 11 — Recovery

Build:

```text
daemon restart
Herdr restart
commander session restart
worker disappearance
Treehouse mismatch
delivery interruption
```

Then crash-injection suite.

---

## Milestone 12 — Mission intelligence

Add:

```text
mission digest
project knowledge
knowledge candidates
execution budgets
```

No autonomous policy refinement.

---

# 62. V1 acceptance test

The canonical demo:

Operator says:

> The invitation flow occasionally allows duplicate acceptance. Find the cause, fix it, have another agent review the change, and open a PR if it passes validation.

Codex commander:

1. creates a scout or implementation task;
2. chooses Codex;
3. Sophon acquires a Treehouse lease;
4. Herdr starts Codex;
5. Codex identifies and fixes the race;
6. Codex commits;
7. Codex sends structured completion;
8. Sophon verifies SHA and lease;
9. commander creates a review task for Claude;
10. Claude identifies a missing concurrency test;
11. Sophon wakes the original Codex worker;
12. Codex adds the test;
13. only changed validation is rerun;
14. no-mistakes runs;
15. no-mistakes passes;
16. PR is created;
17. Sophon records exact PR + SHA;
18. worker becomes idle/inactive;
19. mission becomes completed;
20. Codex presents the operator with:
    - what was wrong;
    - what changed;
    - validation evidence;
    - PR URL;
    - remaining risk.

During this entire sequence, restart `sophond` once.

The outcome must be identical.

---

# 63. What makes Sophon different

The product is not:

> "spawn a bunch of coding agents."

The product is:

> **a deterministic operating system for coordinating probabilistic software agents.**

The commander agent may dynamically decide:

```text
what to investigate
how to decompose
which workers to use
when to seek another opinion
how to synthesize findings
```

Sophon deterministically decides:

```text
what task exists
which attempt is current
which worker owns it
which worktree is valid
what state the task occupies
whether completion evidence is valid
whether validation has passed
whether delivery is authorized
whether cleanup is safe
```

That boundary is the product.

---

# 64. V1 product statement

**Sophon coordinates multiple coding agents as one reliable engineering system.**

The commander agent provides persistent high-level reasoning; Pi, Claude Code, and Codex are supported as co-equal commander runtimes and durable specialist workers.

Herdr keeps their sessions alive and visible.

Treehouse gives every task isolated Git state.

no-mistakes verifies the final result.

Sophon owns the durable execution graph connecting them.

The objective is not maximum agent autonomy.

The objective is:

> **maximum useful autonomy inside deterministic boundaries.**
