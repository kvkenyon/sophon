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

## Reconcile before intervention

Treat worker-session loss as a presence fact, not proof that task work or validation disappeared.
Query the current task, attempt, lease, worker-session, validation, and delivery state through the Python commander API.
An authoritative validation or delivery operation tied to the current head remains authoritative even when the worker session is lost.
Handle its current state rather than creating a duplicate owner.

Recover only the commander's recorded direct worker and current attempt.
Do not search for similarly named sessions or claim unrelated work.
Confirm the recorded Treehouse lease and worktree still match the task and attempt.
Never release by path, replace a mismatch, or allocate a fresh worktree while the existing lease is unresolved.

Preserve uncommitted changes, commits, branch, base SHA, task identity, and attempt identity.
If ownership or worktree availability cannot be reconciled, leave state intact and report the conflicting evidence and preserved-work consequence.

## Escalation order

1. Inspect targeted structured state and the runtime's bounded current evidence.
2. If the worker is waiting on a question its brief answers, send one concise clarification with `await intellect.message_worker(task_id, message)`.
3. If it is confused or looping, use the verified adapter's least disruptive interruption and send one corrective message.
4. If it is genuinely lost or remains unresponsive, request the control plane's supported recovery for the same task and attempt.
   `TODO(spec-gap)`: V1 exposes `retry_task` for a new attempt but does not specify a commander API for relaunching the same attempt; do not substitute an invented runtime command.
5. Use `await intellect.retry_task(task_id)` only when structured reconciliation and policy establish that a new attempt is required.
   A retry receives its own lease and completion fence; a stale attempt can never complete it.
6. After a second failed controlled recovery, stop retrying automatically and report the plain failure, evidence, preserved work, and consequence.

A low context reading or an idle worker is not by itself evidence of a wedge.
Recovery never authorizes new scope, a new task, work destruction, or operator contact by the worker.
