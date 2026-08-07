---
name: worker-recovery
description: Agent-only recovery procedure for a lost, unresponsive, looping, repeatedly confused, or unreachable direct worker while preserving attempt, lease, worktree, and unlanded work.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/stuck-crewmate-recovery/SKILL.md. -->

# Worker recovery

Use this skill when a recorded direct worker is lost, unresponsive, looping, repeatedly confused, asks a question already answered by its brief, or cannot receive a message.
Load `agent-adapters` before any runtime-specific intervention.
Use it only for the worker and attempt already recorded on the task. Recovery
restores supervised ownership of accepted work; it never creates a parallel
implementation, broadens scope, or turns runtime symptoms into project facts.

## Reconcile before intervention

Treat worker-session loss as a presence fact, not proof that task work or validation disappeared.
Query the current task, attempt, lease, worker-session, validation, Signal, and
delivery projection through structured APIs. Inspect worker-session state with
`sophon worker inspect TASK --attempt N --json`; use
`sophon task timeline TASK --json` only for durable event evidence and
`sophon status --mission MISSION --json` for the bounded current mission
projection. Do not repeatedly scrape terminal text or reconstruct current state
from the last event.
An authoritative validation or delivery operation tied to the current head remains authoritative even when the worker session is lost.
Handle its current state rather than creating a duplicate owner.

Recover only the commander's recorded direct worker and current attempt.
Do not search for similarly named sessions or claim unrelated work.
Confirm the recorded Treehouse lease and worktree still match the task and attempt.
Never release by path, replace a mismatch, or allocate a fresh worktree while the existing lease is unresolved.

Preserve uncommitted changes, commits, branch, base SHA, task identity, and attempt identity.
If ownership or worktree availability cannot be reconciled, leave state intact and report the conflicting evidence and preserved-work consequence.

Before any disruptive action, answer these questions from structured evidence:

1. Is this still the current attempt, and is the task actually nonterminal?
2. Does the recorded lease still name the same task, attempt, path, branch, and
   conditional return identity?
3. Is a validation or delivery operation already authoritative for the recorded
   head?
4. Is the worker session live, idle, lost, stopped, or structurally missing, and
   is that observation current?
5. Did the last steer reach this same recorded session and attempt?
6. Is there configured restart or retry budget remaining? An unset optional
   budget is not a commander-invented limit; a configured exhausted budget is
   never bypassed.

If the task is terminal, reconcile completion or cleanup rather than recovering
the worker. If validation or delivery is live, preserve its single ownership and
follow that lifecycle. If the lease or head mismatches, stop: a runtime restart
cannot repair repository custody.

## Classify the failure before acting

- **Answered-by-brief wait:** the task is sound and the worker needs one concise
  pointer to instructions it already owns.
- **Correctable confusion or loop:** the worker repeats a mistaken approach but
  remains reachable; one targeted redirect is appropriate.
- **Transport failure:** a message was not accepted; verify session identity and
  state before resending once.
- **Lost or structurally missing session:** repository state may remain valid,
  but the worker placement does not. Prefer a supported same-attempt recovery
  that resumes persisted agent identity in the recorded worktree.
- **Invalid attempt or custody:** stale attempt, lease mismatch, missing
  worktree, unexplained head, or competing owner. Preserve everything and
  escalate; do not improvise recovery.
- **Repeated failed recovery:** the recovery budget or two controlled attempts
  are exhausted. Stop automatic intervention and surface the failure.

## Escalation order

1. Inspect targeted structured state and the runtime's bounded current evidence.
2. If the worker is waiting on a question its brief answers, send one concise clarification through the verified runtime adapter.
3. Confirm that the clarification or steer was accepted by the same current
   session. Do not send a stream of increasingly long prompts to a worker that
   is not receiving them.
4. If it is confused or looping, use the verified adapter's least disruptive
   supported intervention and send one corrective message that names the
   observed divergence, the still-valid outcome, and the next evidence to
   produce. Do not prescribe a speculative patch.
5. If it is genuinely lost or remains unresponsive, inspect its durable
   worker-session state, reserve any configured restart budget through the
   owning operation, and prefer a supported same-attempt resume in the existing
   worktree with persisted agent identity and a concise progress summary.
   `TODO(spec-gap)`: V1 exposes `sophon task retry TASK --db PATH` for a new attempt but does not specify a commander API for relaunching the same attempt; do not substitute an invented runtime command.
6. Use `await sophon.retry_task(task_id)` or
   `sophon task retry TASK --db PATH` only when structured reconciliation
   and policy establish that a new attempt is required. Retry must fence the old
   attempt before allocating the next attempt and lease. A stale attempt can
   never complete, validate, deliver, or release the new one.
7. Verify the postcondition after each intervention: same task, expected current
   attempt, valid lease, one worker owner, and accepted work or a precise new
   blocker. A launch acknowledgement or a new pane alone is not recovery.
8. After a second failed controlled recovery, or when configured budget is
   exhausted earlier, stop retrying automatically and report the plain failure,
   evidence, preserved work, and consequence.

A low context reading or an idle worker is not by itself evidence of a wedge.
Recovery never authorizes new scope, a new task, work destruction, or operator contact by the worker.

## Recovery handoff

After a successful recovery, send only the minimum durable context the resumed
worker lacks: accepted objective, current attempt, progress already evidenced,
the last verified head, unresolved blocker if any, and the next acceptance
result. Do not rewrite the brief, erase contradictory evidence, or pretend the
restart reset task history. Resume ordinary event-driven supervision
immediately.

After failed recovery, leave all work and custody records intact. Tell the
operator what outcome stopped, what evidence proves recovery failed, whether
unlanded work is preserved, and what decision or supported external repair is
needed. Describe project consequences, not runtime mechanics, unless a concrete
tool or path is required for the operator to act.
