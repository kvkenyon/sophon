---
name: status
description: Fresh snapshot for /status, a status report, catch-up, or a request to see current work; current state comes only from sophon status, after any routine verification/validation drain.
user-invocable: true
metadata:
  internal: true
---

# Status

Generate a complete bounded current snapshot so the operator can resume in one read.
Except for the routine drain below, this skill is read-only.
It never spawns, retries, steers, answers a decision, delivers, releases a lease, or mutates any record beyond that drain.

## Gather

Run `sophon status --json` at invocation time and use its derived result as the snapshot authority.
When the result lists verify-complete or validate actions, those are commander-owned routine work, not discretionary observations: drain them to the fixed point first (verify every ready task, run every pending configured validation, re-deriving between steps), then run `sophon status --json` once more so the snapshot reflects the drained state.
Do not supplement the derived result with chat history, wake-line prose, worker output, filesystem scanning, repository probes, or ad hoc GitHub queries.
Task state is derived at read time: `queued`, `active` (augmented by live pane observation into `running`, `idle`, `lost`, or `unknown-pane`), `ready`, `attention`, `invalid-evidence`, `verified`, first-delivery `delivered`, open-PR `awaiting-feedback`, the correction sequence `correction-pending`, `correction-under-way`, `correction-ready`, `correction-verified`, `correction-awaiting-delivery`, and `correction-validation-failed`, plus `reconciliation`, terminal `merged`, and historical `released`.
Normal `sophon status` is operational and keeps an exact open PR visible even after its worker copy is released. It filters other released tasks plus released-only missions. Use `sophon status --all` for the complete revision/attempt/delivery chain; `delivery_state` distinguishes released-delivered from released-undelivered without implying release delivered anything.
Wake lines are notifications, never state; do not quote them as truth.
Apart from that drain, do not turn an observation into an action from inside this skill.

## Response contract

Render exactly these four sections in this order, each present even when empty:

1. **Needs Your Attention** — only a decision, verified work awaiting your delivery approval, a credential or permission need, or a task stuck after failed recovery.
   Empty state: "Nothing needs your attention right now."
2. **Recently Completed** — the bounded recent verified, delivered, and exact open-PR-awaiting-feedback tasks from the current snapshot.
   Empty state: "No recent completions are in the current snapshot."
3. **Underway** — active work progressing without operator action, plus any `ready` task whose verification just failed (reported with its concrete evidence), one concise line per task.
   Empty state: "Nothing is underway."
4. **Up Next** — queued tasks not yet spawned.
   Empty state: "Nothing is queued."

Put every item in exactly one section.
Valid `attention` reports and `invalid-evidence` belong in Needs Your Attention. Preserve disclosed dirty work and ask only for the concrete unresolved decision. Running, idle, ready, and released tasks never belong there.
Delivered work and an open PR awaiting feedback never belong in Underway. A correction under way does.
Worker-pane idleness is not task completion.
Include every PR as a complete `https://...` URL.
Keep secrets and sensitive values out of the response.
