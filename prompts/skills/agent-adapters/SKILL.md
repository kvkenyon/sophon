---
name: agent-adapters
description: Agent-only reference for selecting, starting, steering, recovering, interrupting, resuming, and verifying Parallel Intellect worker runtimes through supported adapters.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/harness-adapters/SKILL.md. -->

# Agent adapters

Use this reference before selecting, starting, steering, recovering, interrupting, resuming, or verifying a worker runtime, and before handling a runtime trust or readiness condition.
V1 worker adapters are Pi, Claude, and Codex.
Never launch an unverified adapter or silently substitute a different runtime when the selected runtime is part of accepted intent.

## Selection and launch

Select the worker explicitly when creating a task with `await intellect.create_task(..., worker=...)`.
Apply an explicit operator choice first, then any matching configured project or task policy, then the supported default.
Account for every configured candidate using current adapter support, authentication, capacity, model availability, task fit, and disclosed uncertainty.
Do not guess provider identity, credential ownership, model support, or reasoning capability from a name.

Use the selected adapter's authoritative current catalog to validate model and reasoning options.
Unreachable discovery is uncertainty, not proof of support or rejection.
Concrete catalog rejection blocks that configuration.
Do not silently lower requested reasoning quality solely to conserve capacity.

`TODO(spec-gap)`: V1 does not define commander-facing model, reasoning-effort, candidate-ranking, or catalog-query fields for worker creation.
Until it does, pass only supported task fields and let the configured adapter own defaults rather than inventing parameters.

The control plane owns allocation, Treehouse lease identity, task attempt, worktree, runtime session, and generated prompt composition.
The commander must not launch a worker outside `create_task` or take over its repository.

## Lifecycle and delivery checks

A successful send or start acknowledgement is not proof that the worker accepted the brief.
Verify the adapter-specific structured postcondition: correct task and attempt binding, readiness, accepted prompt, and active or valid idle state.
Trust prompts and other interactive conditions require the verified adapter procedure and the minimum necessary action.

Use `await intellect.message_worker(task_id, message)` for ordinary task-scoped steering.
Use `worker-recovery` before disruptive recovery.
Use `await intellect.retry_task(task_id)` only for a controlled new attempt after lease and ownership reconciliation.
Use `await intellect.cancel_task(task_id)` only under the task's cancellation authority and preserve unlanded work.

`TODO(spec-gap)`: V1 does not define commander-facing same-attempt interrupt, exit, resume, trust-confirmation, or readiness APIs.
Do not invent terminal keystrokes or direct process commands; surface the unsupported lifecycle need and preserve task state.

Validation invocation belongs to the worker's runtime overlay and generated brief.
Verify that the validation owner remains the same task attempt, the result is tied to the immutable head, and delivery uses the supported control-plane operation.
Never infer success from terminal appearance or prose.

## Adapter verification contract

Before registering a new adapter, verify end to end:

- executable and version discovery;
- safe launch shape and task-prompt delivery;
- supported model and reasoning options;
- authentication and trust behavior;
- semantic busy, idle, inactive, lost, and stopped evidence;
- steering acknowledgement;
- interrupt, cancellation, exit, resume, and persistence behavior;
- completion and blocker command availability;
- lease and attempt binding; and
- validation ownership and delivery postconditions.

Record the runtime version, exact test surface, evidence, and unsupported facts.
Prefer protocol or process state to rendered UI strings.
Fail safely for unsupported adapters instead of approximating another adapter's behavior.
