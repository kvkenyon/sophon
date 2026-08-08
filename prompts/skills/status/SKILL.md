---
name: status
description: Fresh read-only snapshot for /status, a status report, catch-up, or a request to see current work; current state comes only from sophon status.
user-invocable: true
metadata:
  internal: true
---

# Status

Generate a complete bounded current snapshot so the operator can resume in one read.
This skill is read-only.
It never spawns, retries, steers, answers a decision, verifies, validates, delivers, releases a lease, or mutates any record.

## Gather

Run `sophon status --json` exactly once at invocation time and use its derived result as the snapshot authority.
Do not supplement it with chat history, wake-line prose, worker output, filesystem scanning, repository probes, or ad hoc GitHub queries.
Task state is derived at read time: `queued`, `active` (augmented by live pane observation into `running`, `idle`, `lost`, or `unknown-pane`), `ready`, `verified`, and `delivered`.
Wake lines are notifications, never state; do not quote them as truth.
Do not turn an observation into an action from inside this skill.
Plain `/status` writes nothing.

## Response contract

Render exactly these four sections in this order, each present even when empty:

1. **Needs Your Attention** — only a decision, verified work awaiting your delivery approval, a credential or permission need, or a task stuck after failed recovery.
   Empty state: "Nothing needs your attention right now."
2. **Recently Completed** — the bounded recent verified and delivered tasks from the current snapshot.
   Empty state: "No recent completions are in the current snapshot."
3. **Underway** — active work progressing without operator action, plus `ready` tasks awaiting the commander's own verification, one concise line per task.
   Empty state: "Nothing is underway."
4. **Up Next** — queued tasks not yet spawned.
   Empty state: "Nothing is queued."

Put every item in exactly one section.
Running, idle, and ready tasks never belong in Needs Your Attention.
Delivered work never belongs in Underway.
Worker-pane idleness is not task completion.
Include every PR as a complete `https://...` URL.
Keep secrets and sensitive values out of the response.
