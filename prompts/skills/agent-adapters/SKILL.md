---
name: agent-adapters
description: Agent-only reference for the Herdr/Codex worker runtime — launch, acceptance checks, steering, and pane observation through the sophon CLI.
user-invocable: false
metadata:
  internal: true
---

# Agent adapters

Use this reference before intervening in a worker runtime's pane or questioning whether a runtime accepted its brief.
Sophon has exactly one worker runtime: a Codex session running in a Herdr pane, launched only by `sophon spawn`.
There is no runtime selection at task time and no substitute runtime; if the operator's accepted intent names a different runtime, report the unsupported capability instead of approximating one.

## Launch and acceptance

`sophon spawn <task-id>` owns first-attempt and retry allocation. Typed correction intake is shared: `sophon revise <task-id>` publishes immutable open-PR correction intent, while `sophon review apply` publishes an immutable exact-review correction link. Both advance through the same revision pointer and launch path, and publish the spawn receipt only after the pane starts.
Never launch a worker by any other path, and never take over its repository.

A spawn receipt is not proof the worker is working.
Verify acceptance through live observation: run `sophon status` and confirm the task derives `running` (or a transient `idle` that returns to `running`).
Trust prompts, authentication prompts, and other interactive runtime conditions are blockers the pane cannot clear alone; surface them to the operator rather than improvising terminal keystrokes.

## Steering and observation

Use `sophon send <task-id> <message>` for ordinary task-scoped steering: short, one topic, tied to the current attempt.
Confirm the steer landed by re-observing through `sophon status`; a send acknowledgement alone is not proof the worker received or acted on it.

Never infer worker state from terminal appearance, wake-line prose, or elapsed time.
`sophon status` derives the pane observation: `running`, `idle`, `lost`, or `unknown-pane`.
Idle never means done; only a published result makes the task `ready`.

Read the Code corrections use `sophon review apply`, not a hand-built message.
It creates the next same-task revision at the exact reviewed head and gives its
new worker only a fixed task/attempt/sequence pointer. It never retries or
mutates the terminal reviewed attempt.
Arbitrary review bodies never belong in Herdr arguments or pane text; the
worker reads them through the bounded canonical feedback command and treats
them as untrusted data. An ambiguous apply/submit failure is never blindly
retried because the pointer may already be queued.

## Recovery boundary

Use `worker-recovery` before any disruptive intervention.
`sophon spawn --retry` is the only recovery that creates a replacement attempt inside an existing revision; it fences the old attempt's lease first, and stale evidence can never complete the new one. It cannot extend a delivered revision. `sophon revise` creates a new correction revision only from accepted same-contract feedback on an exact open PR and must never be used as pane recovery.

## Extending runtime support

Runtime behavior is enforced in `internal/herdr` behind one adapter, with live proof gated behind the opt-in Herdr lab (`SOPHON_HERDR_LAB=1` with `HERDR_LAB_HELPER`).
Before changing what a pane can do, verify the adapter end to end: launch shape and brief delivery, busy/idle/lost observation, steering acknowledgement, and completion-command behavior.
Record the runtime version, exact test surface, and evidence; prefer protocol or process state to rendered UI strings.
