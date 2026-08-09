# Sophon filesystem protocol

Sophon's only state model is a filesystem protocol under the Sophon data home.
There is no daemon, database, event stream, command ledger, state projection,
or managed agent runtime. Facts are canonical typed records; status is derived
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
      task.json                         # durable intent + current_attempt token
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
4. **Attempt fencing.** `task.json.current_attempt` is the incarnation token.
   `sophon spawn` writes attempt 1; `sophon spawn --retry` fences the previous
   attempt's lease (conditional release by exact lease id + holder, best
   effort), bumps `current_attempt`, and spawns the next attempt. Verification
   and delivery act only on the current attempt; a stale attempt's result is
   refused loudly without mutating current work.
5. **Derived status.** Task state is computed at read time from the exact
   current attempt. A valid exact-identity `release.json` → historical
   `released`; a valid `report.json` → `attention`; a malformed, mismatched, or
   conflicting canonical completion/report/release artifact →
   `invalid-evidence`; terminal `delivery.json` → `delivered`; `outcome.json` →
   `verified`; a schema-valid completed `result.json` without outcome → `ready`;
   otherwise the worker pane is observed live → `running`/`idle`/`lost`.
   File presence alone never makes completion ready. Wake lines in
   `state/` are notifications only; absent, duplicated, or contradictory wake
   lines never change derived status. A result completed while no commander
   session exists simply waits on disk and surfaces as `ready` or `attention`
   to the next session — no recovery transition exists. A fenced attempt's
   result, report, or release never affects the current attempt.
   The same derivation also yields the commander action queue: every `ready`
   task maps to an exact `sophon verify-complete <task-id>` action and every
   `verified` task whose configured validation has no receipt yet maps to an
   exact `sophon validate <task-id>` action, printed by `sophon status` as
   the commands to run (verify actions first). An existing validation
   receipt — pass or fail — is terminal for the queue; a failed validation is
   correction routing, never a blind re-run. `attention`, `invalid-evidence`,
   and `released` never emit automated actions.
   Normal `sophon status` is the operational view: it omits released tasks and
   missions containing only released tasks. `sophon status --all` retains every
   durable mission/task and labels released current attempts explicitly;
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
7. **Running steering and worker pane retirement.** `sophon send` observes the
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
   available for corrections to the same attempt.
8. **External boundaries.** The lease boundary uses exact identity guards:
   release is conditional on `(lease_id, holder)` and fences on mismatch. The
   forge boundary pushes an exact SHA and find-or-creates the PR by
   repository + branch + SHA. Where an external effect creates a real crash
   window, the command writes typed intent before the effect and a typed
   receipt after (`delivery.json`, `release.json`); re-running the same command
   converges to the same result via observed reality. There is no generalized
   command ledger.
9. **Authority.** A live commander session may verify and validate
   autonomously. Every delivery effect requires operator confirmation, enforced
   mechanically: `sophon deliver` refuses without `--confirmed`. With no
   commander session alive, nothing advances and nothing is lost.

## CLI

```text
sophon version
sophon mission create --project <path> --title <t> --objective <o>
sophon mission list [--json]
sophon task create --mission <id> --title <t> [--kind implementation]
                   [--delivery branch|pr] [--validate <command>]
sophon commander attach [--pane <id>] [--workspace <id>] [--tab <id>]
sophon spawn <task-id> [--retry]
sophon worker complete <task-id> --attempt <n> --head-sha <sha> --result <path>
sophon worker report <task-id> --attempt <n> --head-sha <sha> --report <path>
sophon verify-complete <task-id>
sophon validate <task-id>
sophon deliver <task-id> --confirmed
sophon release <task-id>
sophon status [--json] [--all]
sophon send <task-id> <message>
sophon prompt commander
```

Binary paths for external tools (`--herdr`, `--treehouse`, `--git`, `--gh-axi`)
are flags on the commands that need them, defaulting to PATH lookup.

Spawn resolves the data home once to a clean absolute path and propagates
that exact non-secret value two ways: as a `SOPHON_DATA_HOME` environment
assignment on the worker runtime's launch command (all supported runtimes),
and pinned into the brief's generated completion and report commands. A worker therefore
publishes to the assigned store even when its runtime drops inherited
environment; no other environment values cross the launch boundary.

## Explicit limitations

- Sophon does not move unless a commander session (or the operator) invokes
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
- Validation is re-run on demand; there is no content-addressed cache.
- Single operator, single machine. No multi-machine, remote workers, or
  auto-merge.
