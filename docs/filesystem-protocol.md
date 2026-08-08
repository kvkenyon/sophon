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
        result.json                     # worker's only write (via sophon worker complete)
        validation.json                 # validation receipt (sophon validate)
        outcome.json                    # verified-completion receipt (sophon verify-complete)
        delivery.json                   # delivery intent + receipt (sophon deliver)
        release.json                    # conditional lease-release receipt (sophon release)
  state/
    <task-id>.status                    # VOLATILE wake lines; never truth
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
3. **Worker write surface.** A worker publishes only its own attempt-scoped
   `result.json` (through `sophon worker complete`, which validates the strict
   schema and publishes atomically) and appends wake lines to its own
   `state/<task>.status` notification file. Workers never mutate mission/task
   intent, the current-attempt token, outcomes, delivery records, or any other
   shared lifecycle state.
4. **Attempt fencing.** `task.json.current_attempt` is the incarnation token.
   `sophon spawn` writes attempt 1; `sophon spawn --retry` fences the previous
   attempt's lease (conditional release by exact lease id + holder, best
   effort), bumps `current_attempt`, and spawns the next attempt. Verification
   and delivery act only on the current attempt; a stale attempt's result is
   refused loudly without mutating current work.
5. **Derived status.** Task state is computed at read time:
   no attempts → `queued`; current attempt has `delivery.json` in a terminal
   state → `delivered`; `outcome.json` present → `verified`; `result.json`
   present without `outcome.json` → `ready` (pending verification); otherwise
   the worker pane is observed live → `running`/`idle`/`lost`. Wake lines in
   `state/` are notifications only; absent, duplicated, or contradictory wake
   lines never change derived status. A result completed while no commander
   session exists simply waits on disk and surfaces as `ready` to the next
   session — no recovery transition exists.
6. **External boundaries.** The lease boundary uses exact identity guards:
   release is conditional on `(lease_id, holder)` and fences on mismatch. The
   forge boundary pushes an exact SHA and find-or-creates the PR by
   repository + branch + SHA. Where an external effect creates a real crash
   window, the command writes typed intent before the effect and a typed
   receipt after (`delivery.json`, `release.json`); re-running the same command
   converges to the same result via observed reality. There is no generalized
   command ledger.
7. **Authority.** A live commander session may verify and validate
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
sophon spawn <task-id> [--retry]
sophon worker complete <task-id> --attempt <n> --head-sha <sha> --result <path>
sophon verify-complete <task-id>
sophon validate <task-id>
sophon deliver <task-id> --confirmed
sophon release <task-id>
sophon status [--json]
sophon send <task-id> <message>
sophon prompt commander
```

Binary paths for external tools (`--herdr`, `--treehouse`, `--git`, `--gh-axi`)
are flags on the commands that need them, defaulting to PATH lookup.

## Explicit limitations

- Sophon does not move unless a commander session (or the operator) invokes
  commands. Completed work waits safely and visibly; nothing automatically
  recovers, restarts, or fails over any agent session.
- The lock's stale reclamation relies on pid liveness on a single machine;
  pid reuse within a concurrent command lifetime is accepted as conservative
  refusal, never silent takeover.
- `state/*.status` wake lines are best-effort notifications. Liveness of
  notification is a commander-prompt concern, not a correctness mechanism.
- Validation is re-run on demand; there is no content-addressed cache.
- Single operator, single machine. No multi-machine, remote workers, or
  auto-merge.
