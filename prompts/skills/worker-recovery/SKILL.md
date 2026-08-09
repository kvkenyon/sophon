---
name: worker-recovery
description: Agent-only recovery procedure for a lost, unresponsive, looping, repeatedly confused, or unreachable worker while preserving attempt, lease, worktree, and unlanded work.
user-invocable: false
metadata:
  internal: true
---

# Worker recovery

Use this skill when a task derives `lost` or `unknown-pane`, when a worker is unresponsive, looping, repeatedly confused, asks a question already answered by its brief, or after a steer fails.
Load `agent-adapters` before any runtime-specific intervention.
Recovery restores supervised ownership of accepted work; it never creates a parallel implementation, broadens scope, or turns runtime symptoms into project facts.

## Reconcile before intervention

Treat pane loss as a presence fact, not proof that task work disappeared.
Run `sophon status --json` for the current derivation, then read the current attempt's records — `spawn.json`, `result.json`, `report.json`, `outcome.json`, and `release.json` — before touching anything.
A valid `attention` report is decision or blocker reconciliation on the preserved attempt, not lost-worker recovery. A released task is terminal cleanup and appears only under `sophon status --all`.
A task that already derives `ready` or beyond needs completion review, not recovery.
Never scrape terminal text or reconstruct current state from wake lines.

Confirm the recorded lease and worktree still match the current task and attempt.
Never release by path, replace a mismatch, or allocate a fresh worktree while the recorded one is unresolved.
Preserve uncommitted changes, commits, branch, base SHA, task identity, and attempt identity.
If ownership cannot be reconciled, leave state intact and report the conflicting evidence and preserved-work consequence.

Before any disruptive action, answer these questions from structured evidence:

1. Is this still the current attempt, and is the task actually nonterminal?
2. Does the recorded spawn receipt still name the same task, attempt, worktree, branch, and lease identity?
3. Is a result, outcome, validation, or delivery receipt already authoritative for this attempt?
4. Is the pane observation current — `running`, `idle`, `lost`, or `unknown-pane`?
5. Did the last steer reach this same recorded pane and attempt?

If the task is terminal, reconcile completion or cleanup rather than recovering the worker.
If the lease or head mismatches, stop: a runtime restart cannot repair repository custody.

## Classify the failure before acting

- **Answered-by-brief wait:** the task is sound and the worker needs one concise pointer to instructions it already owns.
- **Correctable confusion or loop:** the worker repeats a mistaken approach but remains reachable; one targeted redirect is appropriate.
- **Transport failure:** a message was not accepted; verify pane identity and state before resending once.
- **Lost or unknown pane:** repository state may remain valid, but the worker placement does not; reconcile custody first.
- **Invalid attempt or custody:** stale attempt, lease mismatch, missing worktree, unexplained head, or competing owner. Preserve everything and escalate; do not improvise recovery.
- **Repeated failed recovery:** two controlled interventions have failed. Stop automatic intervention and surface the failure.

## Escalation order

1. Inspect targeted structured state and the live pane observation.
2. If the worker is waiting on a question its brief answers, send one concise clarification through `sophon send <task-id> <message>`.
3. Confirm through `sophon status` that the steer was accepted by the same current pane. Do not send a stream of increasingly long messages to a worker that is not receiving them.
4. If it is confused or looping, send one corrective message that names the observed divergence, the still-valid outcome, and the next evidence to produce. Do not prescribe a speculative patch.
5. If the pane is genuinely lost or remains unresponsive, use `sophon spawn <task-id> --retry` only when reconciliation establishes that a new attempt is required. Retry fences the old attempt's lease by exact identity before allocating the next attempt, branch, and worktree. A stale attempt can never verify, validate, deliver, or release the new one; its records are preserved evidence.
6. Verify the postcondition after each intervention: same task, expected current attempt, valid lease, one worker owner, and accepted work or a precise new blocker. A launch acknowledgement or a new pane alone is not recovery.
7. After a second failed controlled recovery, stop retrying automatically and report the plain failure, evidence, preserved work, and consequence.

An idle pane is not by itself evidence of a wedge.
Recovery never authorizes new scope, a new task, work destruction, or operator contact by the worker.

## Recovery handoff

After a successful retry, the new attempt's generated brief already carries the task's durable intent; steer only the minimum the fresh worker lacks — progress already evidenced, the last verified head if any, and an unresolved blocker if one exists.
Do not erase contradictory evidence or pretend the retry reset task history.
Resume ordinary supervision immediately.

After failed recovery, leave all work and custody records intact.
Tell the operator what outcome stopped, what evidence proves recovery failed, whether unlanded work is preserved, and what decision or external repair is needed.
Describe project consequences, not runtime mechanics, unless a concrete tool or path is required for the operator to act.
