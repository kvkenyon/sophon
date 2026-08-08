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

`sophon spawn <task-id>` owns lease acquisition, branch creation, brief generation, and pane launch, and publishes the spawn receipt only after the pane starts.
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

## Recovery boundary

Use `worker-recovery` before any disruptive intervention.
`sophon spawn --retry` is the only recovery that creates a new attempt; it fences the old attempt's lease first, and a stale attempt can never complete the new one.

## Extending runtime support

Runtime behavior is enforced in `internal/herdr` behind one adapter, with live proof gated behind the opt-in Herdr lab (`SOPHON_HERDR_LAB=1` with `HERDR_LAB_HELPER`).
Before changing what a pane can do, verify the adapter end to end: launch shape and brief delivery, busy/idle/lost observation, steering acknowledgement, and completion-command behavior.
Record the runtime version, exact test surface, and evidence; prefer protocol or process state to rendered UI strings.
