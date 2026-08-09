# Sophon filesystem protocol

Sophon's only state model is a filesystem protocol under the Sophon data home.
There is no database, command ledger, state projection, or managed agent
runtime. One intentionally narrow local monitor may carry optional notification
traffic; it never carries facts or authority. Facts are canonical typed records; status is derived
at read time from those records plus live Herdr, Treehouse, Git, and forge
observation.

Roles: the **operator** (human), the **commander** (an ordinary, unmanaged,
disposable agent session that is the operator's sole point of contact and the
only planner), and **workers** (isolated agent sessions that execute one
attempt each).

## Layout

```text
~/.sophon/                              # data home (SOPHON_DATA_HOME overrides)
  missions/<mission-id>/
    mission.json                        # durable intent: project path, objective
    tasks/<task-id>/
      task.json                         # public contract + current_revision/current_attempt pointers
      revisions/<n>/
        correction.json                # immutable accepted open-PR correction intent (n > 1)
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
  state/
    <task-id>.status                    # VOLATILE wake lines; never truth
    commander.json                      # VOLATILE commander wake/placement address; never truth
    .lock/                              # shared-mutation lock (mkdir) with owner.json
    monitor/                            # private optional notification runtime (0700)
      rpc.sock                          # JSON-RPC 2.0 Unix socket (0600)
      runtime.json                      # private exact generation/pid identity (0600)
      start.lock                        # crash-released singleton file lock (0600)
      monitor.log[.1]                   # bounded transport diagnostics, never bodies/tokens
  skills/                               # per-session materialized runtime skills
```

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
   only the attempt. It cannot extend a delivered revision. `sophon revise`
   is the sole next-revision owner: after exact open-PR observation it publishes
   immutable `correction.json`, advances both pointers, then allocates at that
   recorded head. An exact retry can recover the narrow intent-before-pointer
   crash window; differing pending intent refuses. A retry inside a correction
   revision reuses its immutable correction base. Stale revision/attempt
   evidence is preserved and refused.
5. **Derived status.** Task state is computed at read time from the exact
   current attempt. A valid exact-identity `release.json` → historical
   `released`; a valid `report.json` → `attention`; a malformed, mismatched, or
   conflicting canonical completion/report/release artifact →
   `invalid-evidence`; terminal first `delivery.json` → `delivered`; an exact
   observed open PR → `awaiting-feedback`; correction intent/spawn/result/
   outcome/validation derive `correction-pending`, `correction-under-way`,
   `correction-ready`, `correction-verified`, and
   `correction-awaiting-delivery`; drift → `reconciliation`; a merged PR →
   `merged`; otherwise `outcome.json` → `verified`; a schema-valid completed
   `result.json` without outcome → `ready`;
   otherwise the worker pane is observed live → `running`/`idle`/`lost`.
   File presence alone never makes completion ready. Wake lines in
   `state/` are notifications only; absent, duplicated, or contradictory wake
   lines never change derived status. A result completed while no commander
   session exists simply waits on disk and surfaces as `ready` or `attention`
   to the next session — no recovery transition exists. A fenced attempt's
   result, report, or release never affects the current attempt.
   The same derivation also yields the commander action queue: every `ready`
   or `correction-ready` task maps to an exact `sophon verify-complete
   <task-id>` action and every `verified` or `correction-verified` task whose
   configured validation has no receipt yet maps to an
   exact `sophon validate <task-id>` action, printed by `sophon status` as
   the commands to run (verify actions first). An existing validation
   receipt — pass or fail — is terminal for the queue; a failed validation is
   correction routing, never a blind re-run. `attention`, `invalid-evidence`,
   and `released` never emit automated actions.
   Normal `sophon status` is the operational view: it keeps an exact open PR
   visible even when its worker copy is released, and omits other released
   tasks/missions. `sophon status --all` retains the complete revision/attempt
   chain and labels released attempts explicitly;
   `delivery_state` distinguishes `not-delivered` from terminal delivery
   receipts without implying release performed delivery. No record is deleted
   or compacted. `sophon mission list` remains durable mission history.
6. **Volatile commander routing.** `sophon commander attach` records the live
   commander's exact Herdr session/workspace/tab/pane in
   `state/commander.json`. The record is liveness and presentation routing
   only: after `sophon worker complete` durably publishes `result.json` or
   `sophon worker report` durably publishes `report.json`, the
   CLI best-effort submits a fixed Sophon-generated wake (exact task identity
   and commands, with an unambiguous instruction to drain derived
   verification, required validation, and status before replying or waiting)
   to the registered pane, and `sophon spawn` groups a new worker as a tab in
   the registered workspace of the same explicit Herdr session. A missing, malformed, stale, dead, or duplicate target is a
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
   available for recovery within the same attempt; accepted feedback after
   delivery allocates a new revision and worker at the exact open-PR head.
9. **External boundaries.** The lease boundary uses exact identity guards:
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
10. **Authority.** A live commander session may verify and validate
   autonomously. Every delivery effect requires operator confirmation, enforced
   mechanically: `sophon deliver` refuses without `--confirmed`. With no
   commander session alive, nothing advances and nothing is lost.

## CLI

```text
sophon version
sophon mission create --project <path> --title <t> --objective <o>
sophon mission list [--json]
sophon task create --mission <id> --title <public-title>
                   --objective <worker-objective>
                   --delivery-branch <public-branch> [--kind implementation]
                   [--delivery branch|pr] [--validate <command>]
sophon commander attach [--pane <id>] [--workspace <id>] [--tab <id>]
sophon monitor run|start [--herdr <path>]
sophon monitor status [--json]
sophon monitor stop
sophon spawn <task-id> [--retry]
sophon worker progress <task-id> --attempt <n> --phase <phase> [--message <note>]
sophon revise <task-id> --reason <same-contract-reason>
                    --objective <bounded-correction> [--accept-external-head]
sophon worker complete <task-id> --attempt <n> --head-sha <sha> --result <path>
sophon worker report <task-id> --attempt <n> --head-sha <sha> --report <path>
sophon verify-complete <task-id>
sophon validate <task-id>
sophon deliver <task-id> --confirmed
sophon release <task-id> [--attempt <n>]
sophon status [--json] [--all]
sophon send <task-id> <message>
sophon prompt commander
```

Binary paths for external tools (`--herdr`, `--treehouse`, `--git`, `--gh-axi`)
are flags on the commands that need them, defaulting to PATH lookup.

Task creation requires all three distinct intent values. `--title` is a
single printable public line of at most 120 characters (including an issue
key naturally when appropriate); `--objective` is the detailed private work
order; and `--delivery-branch` is a valid, explicitly public-safe Git branch
used by both branch and PR delivery. No field falls back to another. Records
created before this schema remain readable historical evidence. An already
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
