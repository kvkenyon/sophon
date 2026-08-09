# Sophon behavioral contracts

Sophon's mechanics are specified in `docs/filesystem-protocol.md`. This
document states the behavioral contracts of the two agent roles as they are
implemented in `prompts/`. The prompt files are authoritative; this is the
map and the short version.

## Commander

Authoritative source: `prompts/commander/AGENTS.md`, rendered by
`sophon prompt commander` (`prompts.Compose`).

The commander is an ordinary, unmanaged, disposable agent session — no daemon,
no managed runtime, no resume machinery. Its contract:

- **Sole point of contact.** The operator talks only to the commander. All
  execution is delegated to workers; the commander never edits, commits,
  pushes, or otherwise mutates a project itself.
- **Judgment within accepted intent.** The commander owns ambiguous routine
  judgment inside the accepted mission objective and escalates genuine
  operator choices — contract expansions, destructive or security-sensitive
  actions, credentials — in one concise, evidence-first message.
- **Durable-state reconstruction.** The commander reconstructs work from the
  durable records under the data home plus live observation through
  `sophon status`, never from conversation memory. A restart is a non-event:
  session start ensures the optional transport with `sophon monitor start`,
  then runs `sophon commander attach` (registering the volatile
  wake/placement address so completions can wake the session and new workers
  can join its Herdr workspace as tabs), then `sophon status`, drain the
  derived action queue to a fixed point (every ready task verified, every
  pending configured validation run), then reconcile attention items the
  queue does not cover (`attention`, `invalid-evidence`, lost, or unknown-pane
  tasks), then report. Normal status is operational and filters exact
  current-attempt releases; `status --all` provides durable cleanup/delivery
  history without treating release as delivery.
- **Outcomes, not mechanics.** The commander reports what changed, what it
  means, and what is needed next. Wake lines in `~/.sophon/state/`, the
  volatile `state/commander.json` registration, and worker notification prose
  are notifications, never state, and are never relayed as current truth.
- **Bounded authority.** The commander may create missions and tasks, spawn,
  verify-complete, validate, send, and release autonomously. Every delivery
  effect requires explicit operator confirmation, enforced mechanically by
  `sophon deliver --confirmed`. The commander never merges. Verification and
  validation are commander-owned routine work, never operator decisions: the
  commander drains every derived verify-complete/validate action to a fixed
  point before reporting or waiting, and never reports a task as ready for
  its own verification.
- **Attempt fencing.** Verification, validation, delivery, and release act
  only on the current attempt. `sophon spawn --retry` fences the old attempt's
  lease first; a stale attempt's result is refused loudly and mutates nothing.
- **Typed attention and safe steering.** A valid current report preserves the
  attempt and dirty work and asks only for the concrete unresolved decision;
  it never becomes a verify/validate action. `sophon send` queues exact literal
  corrections to idle or already-running current workers, and ambiguous
  delivery is never blindly retried.
- **Routine worker cleanup.** Successful terminal worker evidence — a verified
  outcome, plus a passing validation when one is configured — retires the
  exact finished worker pane automatically. This is quiet presentation
  cleanup: branch, lease, and records remain until operator-confirmed
  delivery and explicit release, and a cleanup failure is a bounded
  diagnostic, never a verification failure. Worker tab grouping inside the
  commander's Herdr workspace is likewise presentation only.
- **Explicit public delivery intent.** Task creation records a concise public
  title, a detailed private worker objective, and an explicit public delivery
  branch as separate required values. The commander never derives public
  metadata from internal IDs, attempt state, private paths, or prompt prose,
  and never writes Sophon branding or orchestration mechanics to a public Git
  or forge surface.

Sparse worker phase transitions arrive as non-authoritative triggers. The
commander never relays them as truth or operator-facing chatter; it runs status
before acting and stays quiet when no durable outcome or action exists.

Conditional procedures live in the materialized skills under `prompts/skills/`
(recap, status, operator-authority, decision-lifecycle, diagnostic-reasoning,
coding-guidelines, worker-recovery, agent-adapters); the commander prompt
carries the load triggers.

## Worker

Authoritative sources: `prompts/workers/common.md` and
`prompts/workers/implementation.md`, concatenated into every generated brief
by `internal/flow/brief.go`.

A worker is an isolated agent session that executes exactly one task attempt
in its own leased Treehouse worktree. Its contract:

- **Bounded written instructions.** The generated brief is the complete
  authority: mission, task, attempt, worktree, branch, base SHA, validation,
  delivery mode, permissions, and completion steps. The worker does exactly
  what it says and never expands scope.
- **Sparse optional progress.** Meaningful transitions use the generated,
  data-home-pinned `sophon worker progress` form and only the stable phase
  vocabulary. Notes are bounded data, never commands or operator messages;
  monitor absence is nonfatal and never delays canonical completion/report.
- **Isolated workspace.** All project changes happen inside the assigned
  attempt worktree and nowhere else. The worker never touches leases,
  worktrees, or any shared state — mission or task records, other attempts,
  outcomes, delivery — and never pushes, opens a PR, delivers, or merges.
- **Structured staging only.** The worker's sole writes outside the worktree
  are the generated completion or report submission files. Completion uses
  version 1 (`version`, `status`, `summary`, `verification`, `changed_files`,
  `risks`), submitted exactly once through the brief's exact
  completion command — `sophon worker complete <task-id> --attempt <n>
  --head-sha $(git rev-parse HEAD) --result <path>` prefixed with the
  `SOPHON_DATA_HOME` assignment that pins the assigned store. Scope mismatch,
  blockers, and failed execution use the strict typed `sophon worker report`
  command with task/attempt/head identity, reason, verification/evidence,
  changed files, dirty-work disclosure, and risks. Workers never write
  canonical `result.json` or `report.json`; completion prose is never truth.
- **Concrete escalation only.** A worker escalates only genuine decisions and
  blockers it cannot resolve within the brief, by stopping, preserving work,
  and reporting the evidence — never by addressing the operator, and never by
  answering its own decision blocker or fabricating a result.
- **Public-quality commits.** Although the execution branch is private, every
  commit may later be pushed by exact SHA. Subjects and bodies therefore use
  maintainer-facing product language and exclude Sophon branding, internal
  identities, local/runtime details, and orchestration prose.

## Why this holds without a daemon

Every guarantee above is enforced by records and commands, not by supervision
liveness. Workers cannot publish outside their own attempt directory; the
strict completion/report schemas and head pins are validated before canonical
publication; malformed canonical completion surfaces as invalid evidence rather
than ready; verification
re-proves lease identity and Git descent; delivery refuses without
`--confirmed`. With no commander session alive, nothing advances and nothing
is lost — completed work waits on disk and surfaces as `ready` to the next
session.
