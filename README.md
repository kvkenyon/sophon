# Sophon

Sophon is single-operator local orchestration for autonomous coding agents. A human operator talks to one **commander** — an ordinary, disposable agent session; the commander decomposes work into tasks and dispatches **workers**, each an isolated agent session executing one attempt in its own Treehouse worktree over a Herdr pane.

Sophon's only state model is a filesystem protocol: durable typed records under `~/.sophon/`, with task status **derived at read time** from those records plus live Herdr, Treehouse, Git, and forge observation. A deliberately narrow local notification monitor may run in the background, and an opened Read the Code revision may own one blocking external-event bridge. Both transport optional triggers only; neither is task truth, a lifecycle controller, recovery service, or commander manager.

> Reasoning may be probabilistic. Execution state may not be.

## The cast

- **Operator** — the human who sets objectives and retains final authority.
- **Commander** — the agent session the operator talks to; plans, delegates, verifies, and reports.
- **Worker** — an isolated agent session that executes one task attempt.
- **Mission** — the durable objective: project path plus intent.
- **Task** — one substantive product contract with immutable revisions.
- **Revision / attempt** — a verified product increment and its replaceable,
  fenced worker execution.
- **Lease** — Treehouse worktree ownership for one task attempt.

## Install

```bash
go build -o sophon ./cmd/sophon
```

External tools (`herdr`, `treehouse`, `git`, `gh-axi`) are resolved from PATH by default; every command that needs one accepts a flag to override it. Read the Code is deliberately separate and not yet published: configure an explicitly installed or packed `read-the-code-axi` with `--read-the-code PATH` or `SOPHON_READ_THE_CODE=PATH`. Sophon never downloads it or assumes a global package.

## Quickstart

Start a commander session from the rendered prompt (for example, paste the output of `sophon prompt commander` into a fresh agent session), or drive the CLI directly:

```bash
# 0. Ensure the optional per-data-home notification transport is ready.
sophon monitor start

# 1. Create a mission: durable intent for one project.
sophon mission create --project /path/to/repo --title "Rate limiting" \
  --objective "Add per-client rate limiting to the API with regression tests"

# 2. Create a task with separate public metadata and private worker detail.
sophon task create --mission <mission-id> --title "API: add token-bucket limiter" \
  --objective "Implement per-client token-bucket limiting with unit and integration coverage" \
  --delivery-branch "api/token-bucket-limiter" --delivery pr \
  --validate "go test ./..." --review required

# 3. Spawn attempt 1: lease, branch, generated brief, worker pane.
sophon spawn <task-id>

# 4. Watch operational derived state. queued → active → ready → verified → delivered.
#    Status is an action queue first: it also prints the exact next commands
#    (verify-complete for every ready task, validate for every verified task
#    whose configured validation has no receipt yet). Typed worker reports
#    derive attention and invalid canonical evidence derives invalid-evidence;
#    neither emits an automated action.
sophon status

# 5. When the task is ready, prove the attempt and run validation.
sophon verify-complete <task-id>
sophon validate <task-id>

# 6. Deliver only with explicit operator confirmation.
sophon review open <task-id> # required review opens after validation
# Feedback wakes the commander and returns through the derived review actions.
# Exact-head approval is evidence, never delivery confirmation.
sophon deliver <task-id> --confirmed

# 7. If review feedback corrects the same contract while the PR is open,
#    start the next revision at the forge-reported exact PR head.
sophon revise <task-id> --reason "Accepted review correction" \
  --objective "Apply the bounded correction beyond the current PR head"
#    Complete/verify/validate the correction, then obtain separate approval:
sophon deliver <task-id> --confirmed

# 8. Return revision copies independently when they no longer need leases.
sophon release <task-id>

# 9. Inspect the complete immutable revision/attempt history.
sophon status --all
```

Other commands: `sophon monitor run|start|status --json|stop` (direct lifecycle for the optional private JSON-RPC notification transport), `sophon commander attach` (register the live commander's volatile Herdr wake/placement address so publications wake it and workers group into its workspace as tabs), `sophon worker progress` (send one sparse non-authoritative phase transition), `sophon spawn <task-id> --retry` (replace an attempt inside the current revision), `sophon release <task-id> --attempt <n>` (retire one historical copy), `sophon send <task-id> <message>` (queue an exact steer to an idle or running current worker), `sophon mission list` (durable mission history, unlike filtered operational status), `sophon version`.

`sophon review set|open|status|feedback|classify|apply|acknowledge|reconcile|end` owns the local review lifecycle. Review posture is immutable intake (`off`, `optional`, or `required`) with monotonic typed escalation. Canonical attempt records contain only exact session/revision evidence—never the authenticated browser URL—and a crash-released single-owner bridge carries blocking Read the Code events into the existing notification monitor. See [Read the Code integration](docs/read-the-code-review.md).

Workers write completion JSON only to the generated staging path. The CLI validates the complete schema and live head before atomically publishing canonical `result.json`:

```bash
SOPHON_DATA_HOME=<assigned-home> sophon worker complete <task-id> --attempt <n> --head-sha "$(git rev-parse HEAD)" --result <path>
```

Scope mismatch, ordinary blockers, and failed execution use typed non-completion evidence instead of pretending completion:

```bash
SOPHON_DATA_HOME=<assigned-home> sophon worker report <task-id> --attempt <n> --head-sha "$(git rev-parse HEAD)" --report <path>
```

After durable publication the CLI asks the healthy monitor to coalesce and forward the change; when no monitor accepts it, completion/report publication retains the direct commander wake fallback. Notification failure never changes the publication. A valid report derives `attention`, preserves dirty-work disclosure, and creates no verification or validation action. Once terminal completion evidence lands (verification, plus a passing validation when configured), the exact finished worker pane is closed as routine cleanup; the branch and lease remain until explicit release. A valid current-attempt release derives historical `released`; normal status omits it, while `status --all` preserves whether delivery occurred.

## What it guarantees

- **Derived state despite monitor loss.** The optional monitor carries no truth and performs no recovery or lifecycle transitions. With no monitor or commander session alive, nothing advances and nothing is lost — completed work waits on disk and surfaces as `ready` to the next status run.
- **Structured evidence, not prose.** Strict completion and typed non-completion schemas are pinned to the live worktree HEAD. Workers write staging files; only validated CLI publication creates canonical truth. Worker prose and notification lines are never state.
- **Revision and attempt fencing.** Retry stays within a revision; only
  `revise` creates the next correction revision from an exact open-PR head.
  Stale evidence is preserved and refused loudly.
- **Operator authority at the boundary.** Verification and validation are autonomous; every delivery effect requires `--confirmed`. There is no merge path.
- **Exact local review when requested.** Required tasks cannot deliver until the current verified head has a canonical Read the Code approval later than all processed feedback. A new head invalidates old approval, and product cursor/status is rechecked immediately before delivery. Approval never substitutes for confirmation or grants a forge effect.
- **Crash-safe external effects.** Delivery and release write typed intent before the effect and a receipt after; re-running converges to the same result.
- **One continuing open PR.** A confirmed first delivery creates the PR. Each
  accepted same-contract correction needs new confirmation and normally
  fast-forwards the same branch/PR without force or replacement. Merged is
  terminal; closed or drifted identity stops for reconciliation.
- **Operational status plus durable history.** Normal status filters exact current-attempt releases and released-only missions; `status --all` shows immutable history and distinguishes released-delivered from released-undelivered work. No records are deleted.
- **Volatile liveness routing, never truth.** `sophon commander attach` records only a best-effort wake/placement address; publication wakes, grouped worker tabs, and retired worker panes are liveness and presentation, while every fact still derives from files.
- **Hard public boundary.** Task intake records a concise public title and
  explicit public delivery branch separately from the detailed worker
  objective. Delivery pushes the verified SHA to that branch, renders PR
  evidence for maintainers, and refuses before any write if the branch,
  title, body, or commit messages disclose internal branding, identities,
  runtime details, or local paths.

## Documentation

- `docs/filesystem-protocol.md` — the authoritative design: layout, rules, and CLI surface.
- `docs/notification-monitor.md` — versioned JSON-RPC framing, methods, bounds, lifecycle, and security model.
- `docs/read-the-code-review.md` — external CLI contract, immutable review evidence, single-owner bridge, feedback actions, and delivery gate.
- `docs/behavioral-contracts.md` — the commander and worker behavioral contracts implemented in `prompts/`.

Named for the all-seeing sophons in *The Three-Body Problem*.
