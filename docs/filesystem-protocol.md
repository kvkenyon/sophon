# Sophon filesystem protocol

Sophon's only state model is a filesystem protocol under the Sophon data home.
There is no database, command ledger, state projection, or managed agent
runtime. One intentionally narrow local monitor may carry optional notification
traffic, and each opened Read the Code revision may have one blocking external-event
bridge. Neither carries facts or authority. Facts are canonical typed records; status is derived
at read time from those records plus live Herdr, Treehouse, Git, and forge
observation.

Roles: the **operator** (human), the **commander** (an ordinary, unmanaged,
disposable agent session that is the operator's sole point of contact and the
only planner), and **workers** (isolated agent sessions that execute one
attempt each).

An ordinary **workspace root** is a separate organization boundary. It has an
immutable marker and a `projects/` directory, but is never a Git repository,
worker base, data home, registry service, or lifecycle owner. One commander
attaches once at the root and coordinates any number of project-confined tasks.
The global data home remains independently resolved.

## Layout

```text
<workspace-root>/
  .sophon-workspace.json               # immutable identity, canonical root
  projects/
    <project-key>/                      # real direct-child Git repository

~/.sophon/                              # data home (SOPHON_DATA_HOME overrides)
  missions/<mission-id>/
    mission.json                        # intent + immutable workspace/project identity
    tasks/<task-id>/
      task.json                         # development posture, review + revision/attempt pointers
      bootstrap/
        intent.json                     # authority before an empty-root Git mutation
        receipt.json                    # exact empty initial commit/ref
      cancellation.json                 # immutable never-started cancellation/replacement
      delivery-selection.json           # immutable local -> branch/PR selection
      revisions/<n>/
        correction.json                # immutable accepted correction intent (n > 1)
      review-posture/<sequence>.json    # immutable explicit posture escalation history
      attempts/<n>/
        brief.md                        # generated work order (input to worker)
        spawn.json                      # spawn receipt (written by sophon spawn)
        completion-submission.json      # worker staging input; never truth
        report-submission.json          # worker non-completion staging; never truth
        result.json                     # validated canonical completion claim
        report.json                     # validated typed scope-mismatch/blocked evidence
        validation.json                 # validation receipt (sophon validate)
        outcome.json                    # verified-completion receipt (sophon verify-complete)
        delivery.json                   # delivery intent + receipt (sophon deliver)
        release.json                    # conditional lease-release receipt (sophon release)
        review/
          open.json                     # exact product session/base/head; NEVER browser URL/path
          events/<sequence>.json        # contiguous immutable normalized submissions
          decisions/<sequence>.json     # feedback classification; no comment text
          routes/<sequence>.json        # exact-worker correction routing receipt
          approval-acknowledgements/<sequence>.json # commander awareness, not authority
  state/
    <task-id>.status                    # VOLATILE wake lines; never truth
    commander.json                      # VOLATILE commander wake/placement address; never truth
    .lock/                              # shared-mutation lock (mkdir) with owner.json
    monitor/                            # private optional notification runtime (0700)
      rpc.sock                          # JSON-RPC 2.0 Unix socket (0600)
      runtime.json                      # private exact generation/pid identity (0600)
      start.lock                        # crash-released singleton file lock (0600)
      monitor.log[.1]                   # bounded transport diagnostics, never bodies/tokens
    review-bridges/                     # VOLATILE exact kernel-lock owners; never truth
  skills/                               # per-session materialized runtime skills
```

The workspace marker is deliberately outside the data-home protocol because it
describes filesystem scope, not task truth. A workspace mission pins
`workspace_id`, canonical `workspace_root`, canonical `project_key`, resolved
absolute `project_path`, and a filesystem/Git identity fingerprint. Historical
missions containing only an external absolute project path remain readable,
but new workspace work is confined to a real direct child under `projects/`.

## Workspace and project boundary

`sophon workspace init ROOT` creates or validates only the root, marker, and
`projects/`. Initialization is idempotent and refuses unrelated root content,
conflicting or moved markers, unsafe permissions, symlinked roots, nested
workspace ambiguity, and a non-directory projects entry. It does not run Git,
create a remote, install anything, or start a commander.

`project create`, `clone`, `add`, `list`, and `inspect` are direct filesystem
and Git operations, not a project control plane. Keys are canonical lowercase
names. Every read re-resolves the real path and Git common directory and
refuses traversal, symlink escape, case collision, duplicate Git identity,
root-as-project, nested repository identity, rename, or path replacement.
Adoption is explicit and only for a clean, already-confined direct child;
Sophon never moves, deletes, symlinks, or rewrites an external repository.

Project selection is commander judgment with a narrow rule: explicit key
first, a single clear conversational referent second, otherwise ask one
question. Current working directory is never hidden selection authority.
Cross-project outcomes are decomposed into separate project-pinned tasks and
workers; one worker never gets the workspace root as its checkout.

## Proposal, planning, and start

Proposal-only conversation is not represented in this protocol. It stays in
the commander's conversation unless the operator explicitly asks for a draft
artifact. In particular, proposal wording creates no mission/task record,
attempt, lease, Git change, worker, or notification. The commander's embedded
contract is the sole proposal-versus-execution classifier.

After explicit implementation authorization, mission/task creation publishes
durable planning only. A task without an exact current `spawn.json` derives
`planned` and emits the exact `sophon spawn TASK` action. Allocation failure
does not become `active`, and a repeated start reuses that unspawned attempt.
An explicitly confirmed cancellation publishes `cancellation.json`; a planned
revision publishes a replacement task and a cancellation linking the old one.
Neither record is deletion or mutable status.

## Empty repository bootstrap

Local spawn accepts only an exact conventional empty repository: real project
directory, ordinary `.git/`, unborn symbolic HEAD, and no other root entry or
porcelain status including ignored files. Ambient Git routing variables,
linked/unusual layouts, hooks/worktree configuration, submodules, symlinks,
untracked or ignored content, or a wrong top-level refuse without mutation.

Under the shared data-home lock Sophon publishes `bootstrap/intent.json` before
Git mutation. It creates a deterministic empty tree and initial commit and
uses an absent-ref compare-and-swap on the repository's intended local branch.
It then proves the exact root/no-parent commit, empty tree, message and symbolic
ref before publishing `bootstrap/receipt.json` and allocating. Crash recovery
converges from intent, commit, or receipt; a different branch/head refuses.
Bootstrap never creates product files, scaffolding, ignore files, remotes,
public branches, pushes, repositories, or PRs. The worker owns every
substantive descendant commit.

## Development and delivery

`local` is a first-class task delivery posture and the default when no public
branch is supplied. It supports allocation, completion, verification,
validation, exact Read the Code review, and release without a remote. Local
completion preserves its exact commit and is not delivery.

After a remote is separately configured, `delivery select --confirmed`
publishes one immutable local-to-`branch`/`pr` selection only after exact-head,
validation, repository, and public-surface checks. It performs no push or forge
write. A later `deliver --confirmed` is still required for every delivery
effect. `project publish --confirmed` is the separate explicit GitHub
repository/remote operation; no development command calls it or invents an
origin. Existing branch/PR task records retain their behavior.

## Rules

1. **Atomic publication.** Every record is written by temp-file write, sync,
   and rename within the same directory. Readers never see torn records.
2. **One mutator at a time.** All shared mutations happen inside one
   short-lived `sophon` command invocation holding `state/.lock`. The lock is a
   directory containing `owner.json` (pid, command, acquired-at). A lock whose
   owner pid is definitively dead is reclaimed; anything ambiguous fails
   conservatively. There is no CAS, no version column, no conflict reload.
3. **Worker write surface.** A worker writes only its own generated
   attempt-scoped `completion-submission.json` or `report-submission.json`.
   These are non-authoritative staging inputs. `sophon worker complete`
   validates the strict completed schema and live HEAD before atomically
   publishing canonical `result.json`; `sophon worker report` validates the
   strict `scope-mismatch`/`blocked` schema, embedded task/attempt/head identity,
   and live HEAD before atomically publishing canonical `report.json`.
   Report evidence preserves a concise reason, verification/evidence,
   changed-file and dirty-work disclosure, and risks without claiming
   completion. Publication is serialized against retry and sibling evidence:
   identical re-runs converge, while differing same-kind evidence and
   report-versus-completion conflicts refuse. Workers never write canonical
   truth directly or mutate mission/task intent, outcomes, delivery, or release.
4. **Revision and attempt fencing.** The immutable product unit is one verified
   revision; attempts are replaceable executions within it. `task.json` carries
   only `current_revision`/`current_attempt` pointers. `sophon spawn` creates
   revision 1/attempt 1; `spawn --retry` fences the prior exact lease and bumps
   only the attempt. It cannot extend a delivered revision. Typed correction
   intake is the sole next-revision owner: `sophon revise` observes an exact
   open PR, while `sophon review apply` binds classified review sequences to
   their exact local or already-delivered head. Both publish immutable
   `correction.json`, advance both pointers, then allocate at the recorded
   head. An exact retry can recover the narrow intent-before-pointer
   crash window; differing pending intent refuses. A retry inside a correction
   revision reuses its immutable correction base. Stale revision/attempt
   evidence is preserved and refused.
5. **Derived status.** Task state is computed at read time from the exact
   current attempt and re-resolved mission project identity. A replacement,
   rename, or workspace/Git identity mismatch → `project-drift`; a valid
   never-started `cancellation.json` → `cancelled`; no canonical current
   `spawn.json` → `planned`; a valid exact-identity `release.json` → historical
   `released`; a valid `report.json` → `attention`; a malformed, mismatched, or
   conflicting canonical completion/report/release artifact →
   `invalid-evidence`; terminal first `delivery.json` → `delivered`; an exact
   observed open PR → `awaiting-feedback`; correction intent/spawn/result/
   outcome/validation derive `correction-pending`, `correction-under-way`,
   `correction-ready`, `correction-verified`, and
   `correction-awaiting-delivery`; drift → `reconciliation`; a merged PR →
   `merged`; otherwise `outcome.json` → `verified`; a schema-valid completed
   `result.json` without outcome → `ready`;
   otherwise the worker pane from the exact spawn receipt is observed live →
   `running`/`idle`/`lost`. Evidence that claims worker execution without a
   matching spawn receipt is `invalid-evidence`, never active work.
   File presence alone never makes completion ready. Wake lines in
   `state/` are notifications only; absent, duplicated, or contradictory wake
   lines never change derived status. A result completed while no commander
   session exists simply waits on disk and surfaces as `ready` or `attention`
   to the next session — no recovery transition exists. A fenced attempt's
   result, report, or release never affects the current attempt.
   The same derivation also yields the commander action queue: every `planned`
   task maps to exact `sophon spawn <task-id>`; every `ready`
   or `correction-ready` task maps to an exact `sophon verify-complete
   <task-id>` action and every `verified` or `correction-verified` task whose
   configured validation has no receipt yet maps to an
   exact `sophon validate <task-id>` action, printed by `sophon status` as
   the commands to run (verify actions first). An existing validation
   receipt — pass or fail — is terminal for the queue; a failed validation is
   correction routing, never a blind re-run. `attention`, `invalid-evidence`,
   and `released` never emit automated actions.
   Normal `sophon status` is the operational view: it labels each workspace
   task by project key, keeps an exact open PR visible even when its worker
   copy is released, and omits cancelled and other released tasks/missions.
   `sophon status --all` retains the complete revision/attempt/bootstrap/
   cancellation/selection
   chain and labels released attempts explicitly;
   `delivery_state` distinguishes `not-delivered` from terminal delivery
   receipts without implying release performed delivery. No record is deleted
   or compacted. `sophon mission list` remains durable mission history.
6. **Volatile commander routing.** `sophon commander attach --scope ROOT`
   validates the workspace marker and records the live
   commander's exact Herdr session/workspace/tab/pane in
   `state/commander.json` together with the volatile scope identity. The record
   is liveness and presentation routing
   only: after `sophon worker complete` durably publishes `result.json` or
   `sophon worker report` durably publishes `report.json`, the
   CLI best-effort submits a fixed Sophon-generated wake (exact task identity
   and commands, with an unambiguous instruction to drain derived
   verification, required validation, and status before replying or waiting)
   to the registered pane, and `sophon spawn` groups workers from every pinned
   child project as independent tabs in the registered workspace of the same
   explicit Herdr session. A missing, malformed, stale, dead, or duplicate target is a
   bounded diagnostic, never a task failure; spawn then falls back to an
   isolated workspace and the publication still derives from disk. A
   fresh attach replaces only this volatile address — no recovery transition,
   no task-truth mutation. Workers never author the wake prose; the binary
   generates it.
7. **Optional notification monitor.** The monitor is a private per-data-home
   JSON-RPC 2.0 Unix-socket transport, specified in
   `docs/notification-monitor.md`. Canonical publication always precedes a
   durable-change request. An accepted monitor request suppresses the direct
   completion/report wake; an unavailable or rejecting monitor preserves that
   bounded direct fallback. Progress is sparse, attempt-fenced, sanitized, and
   never written as truth. The monitor validates current task/attempt identity
   and canonical change generations before coalescing exact task/attempt bursts
   into fixed Sophon-generated commander triggers. It never polls, replays a
   durable queue, starts or recovers a commander, controls workers, or advances
   lifecycle state. Loss or restart costs optional liveness only; status catches
   up from canonical records.
8. **Running steering and worker pane retirement.** `sophon send` observes the
   exact session/pane from the current attempt's `spawn.json`. Idle delivery
   waits for Herdr's affirmative turn-start acknowledgement; already-running
   delivery submits one queued prompt without waiting for an impossible
   idle-to-working transition and succeeds only on Herdr's acceptance response.
   An error or ambiguous response is never blindly retried, because the exact
   literal message may already be queued. Husk recovery retains native-session
   create-before-close behavior; structurally lost panes refuse. Successful
   terminal worker evidence — a
   verified outcome for a task without validation, or a passing validation
   receipt for a task with one — closes the exact session/tab recorded in the
   current attempt's spawn receipt. Retirement is presentation cleanup only:
   it never changes derived truth, delivery authority, branch or commit
   identity, the lease, or any record. It is idempotent (an already-lost
   exact pane is success), refuses malformed recorded identity, and needs no
   cleanup receipt because the tab close is directly observable and a retry
   converges via reality. Until the terminal boundary the worker pane stays
   available for recovery within the same attempt; accepted feedback allocates
   a new revision and worker at the exact reviewed head. When that head was
   already delivered to an open PR, the correction keeps its exact PR identity.
9. **External boundaries.** Local tasks never consult a forge or require a
   remote. A delivery selection changes only immutable intent and grants no
   external effect. The lease boundary uses exact identity guards:
   release is conditional on `(lease_id, holder)` and fences on mismatch. The
   forge boundary keeps generated execution branches private and pushes an
   exact verified SHA to the explicit public delivery branch. Before any push
   or forge write, the public-surface owner preflights branch identity, concise
   title, curated body, and every outgoing commit message; initial PR bodies
   come only from public task intent plus safe structured result evidence.
   Initial title/body are then human-owned, so corrections preserve them
   rather than blindly overwriting review edits.

   First PR delivery rejects a public branch at another SHA and find-or-creates
   by repository + branch + exact SHA. Correction intake and delivery re-read
   canonical repository/number/URL, base repository/branch, head branch, forge
   head, and remote head. Intake requires an open PR with matching heads,
   publishes typed correction intent before allocation, and creates the private
   branch at that exact SHA. Delivery requires a strict descendant plus a fresh
   `--confirmed`, publishes revision/attempt intent, rechecks immediately, and
   uses an ordinary (never force) fast-forward refspec to the same branch/PR.
   A landed push with a missing receipt converges by observation without a
   duplicate push or PR. Merged is terminal. Closed-unmerged, deleted,
   transferred, base/repository/branch drift, post-intent external changes, or
   non-fast-forward history refuses reconciliation. A reviewed external
   fast-forward before intake may be accepted with
   `revise --accept-external-head` only after Git proves descent.

   External effects with crash windows publish typed intent before the effect
   and a typed receipt after (`delivery.json`, `release.json`); re-running
   converges from observed reality. There is no generalized command ledger.
10. **Authority.** Proposal discussion has zero protocol effects. After
   explicit implementation authority, a live commander session may plan,
   start, verify, validate, review, and release local work autonomously.
   Project publication, local-to-public selection, and every delivery effect
   are distinct explicit operator boundaries, each mechanically refusing
   without its own `--confirmed`. Review approval confirms none of them.
   With no
   commander session alive, nothing advances and nothing is lost.
11. **Read the Code review.** `task.json.review_posture` is `off`, `optional`,
   or `required`; an absent field reads as `off`. Typed task-level changes may
   only escalate posture and preserve immutable history. A review binding is
   published only for the clean current verified head after configured
   validation and contains no browser capability, repository path, executable,
   or process data. Attempt-scoped product events are strictly validated and
   published as contiguous immutable sequence files, so cursor is derived
   rather than mutably advanced. Feedback bodies exist only in those bounded
   records and explicit `review feedback` output; monitor messages, status,
   process arguments, logs, routes, and prompts contain fixed identity/sequence
   pointers only. One crash-released kernel-lock bridge blocks in the external
   versioned CLI's poll, publishes before notifying, and exits when exact
   ownership becomes stale, ended, delivered, or released. It and the monitor
   carry no lifecycle authority. For `required`, delivery also requires a
   canonical approval for the exact current outcome head later than every
   immediate non-capability product-status cursor/revision check. Approval
   never supplies `--confirmed` or any external authority. The full contract
   is in `docs/read-the-code-review.md`.

## CLI

```text
sophon version
sophon workspace init|inspect <root>
sophon project list --workspace <root>
sophon project create <key> --workspace <root> [--initial-branch <branch>]
sophon project clone <key> --workspace <root> --source <git-source>
sophon project add|inspect <key> --workspace <root>
sophon project publish <key> --workspace <root> --repository <owner/repo>
                       --remote-url <url> --visibility private|public|internal
                       --confirmed
sophon mission create --workspace <root> --project <key>
                      --title <t> --objective <o>
sophon mission create --project <historical-absolute-path>
                      --title <t> --objective <o>
sophon mission list [--json]
sophon task create --mission <id> --title <title>
                   --objective <worker-objective>
                   [--delivery local|branch|pr]
                   [--delivery-branch <public-branch>] [--kind implementation]
                   [--validate <command>]
                   [--review off|optional|required]
sophon task cancel <task-id> --reason <reason> --confirmed
sophon task revise <task-id> --title <title> --objective <objective>
                   --confirmed [--validate <command>]
sophon commander attach --scope <root> [--pane <id>] [--workspace <id>] [--tab <id>]
sophon monitor run|start [--herdr <path>]
sophon monitor status [--json]
sophon monitor stop
sophon review set <task-id> --posture optional|required
sophon review open <task-id> [--attempt <n>] [--no-browser] [--json]
sophon review status <task-id> [--json]
sophon review feedback <task-id> [--attempt <n>] [--after <n>] [--limit <n>] [--json]
sophon review classify <task-id> --sequence <n> --disposition requested-changes|non-actionable
sophon review apply <task-id> --sequence <n>
sophon review acknowledge <task-id> --sequence <n>
sophon review reconcile <task-id> [--json]
sophon review end <task-id> [--json]
sophon spawn <task-id> [--retry]
sophon worker progress <task-id> --attempt <n> --phase <phase> [--message <note>]
sophon revise <task-id> --reason <same-contract-reason>
                    --objective <bounded-correction> [--accept-external-head]
sophon worker complete <task-id> --attempt <n> --head-sha <sha> --result <path>
sophon worker report <task-id> --attempt <n> --head-sha <sha> --report <path>
sophon verify-complete <task-id>
sophon validate <task-id>
sophon delivery select <task-id> --mode branch|pr --title <public-title>
                       --branch <public-branch> --confirmed
sophon deliver <task-id> --confirmed
sophon release <task-id> [--attempt <n>]
sophon status [--json] [--all]
sophon send <task-id> <message>
sophon prompt commander
```

Binary paths for external tools (`--herdr`, `--treehouse`, `--git`, `--gh-axi`)
are flags on the commands that need them, defaulting to PATH lookup.
Read the Code is the exception: `--read-the-code` overrides
`SOPHON_READ_THE_CODE`, and an empty configuration refuses with an install/pack
diagnostic rather than assuming PATH, npm publication, or registry access.

Task creation always separates its bounded one-line `--title` from the detailed
private worker `--objective`. A local task needs no public metadata. Branch and
PR tasks additionally require a separately validated public delivery branch;
an immutable local-to-public selection later records both the current public
title and branch. No public value is inferred from the private objective,
internal IDs, local paths, or runtime prose. Records created before this schema
remain readable historical evidence. An already
successful delivery receipt for the exact revision/attempt is returned
unchanged. A historical open PR whose
exact branch predates the public-name sanitizer may be corrected only by
retaining that already-public identity; correction title/body/commits still
pass content preflight, and no new task may create such a branch.
Historical PR receipts that predate recorded base/revision fields remain
immutable: intake verifies their recorded repository/branch/number/URL and
head, reads the missing canonical base identity from the forge, and pins that
complete identity only in the new correction record.

Spawn resolves the data home once to a clean absolute path and propagates
that exact non-secret value two ways: as a `SOPHON_DATA_HOME` environment
assignment on the worker runtime's launch command (all supported runtimes),
and pinned into the brief's generated completion and report commands. A worker therefore
publishes to the assigned store even when its runtime drops inherited
environment; no other environment values cross the launch boundary.

## Explicit limitations

- The monitor has no supported worker turn-end source in the current Herdr
  boundary. `notify.turn_ended` is therefore intentionally omitted: there is
  no screen scraping, private runtime hook, or polling substitute.
- Sophon lifecycle does not move unless a commander session (or the operator) invokes
  commands. Completed work waits safely and visibly; nothing automatically
  recovers, restarts, or fails over any agent session.
- The lock's stale reclamation relies on pid liveness on a single machine;
  pid reuse within a concurrent command lifetime is accepted as conservative
  refusal, never silent takeover.
- `state/*.status` wake lines are best-effort notifications. Liveness of
  notification is a commander-prompt concern, not a correctness mechanism.
- `state/commander.json` is volatile liveness/presentation routing. Nothing
  reads it for truth; deleting it costs only wake delivery and tab grouping.
- Worker tab grouping and pane retirement are presentation only. Layout never
  participates in correctness, and retirement never releases a lease or
  discards work.
- Validation receipts are immutable per attempt. A failed receipt keeps that
  attempt's evidence and worker path; a replacement attempt within the same
  revision is required for a new verified head and validation receipt.
- Single operator, single machine. No multi-machine, remote workers, or
  auto-merge.
