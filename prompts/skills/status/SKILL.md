---
name: status
description: Fresh read-only mission snapshot for /status, a status report, catch-up, or a request to see current work; current state comes only from Parallel Intellect.
user-invocable: true
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/bearings/SKILL.md. -->

# Status

Generate a complete bounded current snapshot so the operator can resume in one read.
This skill is read-only.
It never cleans up, dispatches, retries, steers, answers a decision, validates, delivers, releases a lease, or mutates mission or task state.

## Gather

Call `pintellect status [--mission ID] --json` exactly once at invocation time and use its structured result as the snapshot authority. When more than one mission exists, supply the selected mission ID; a missing mission returns the valid empty snapshot.
Do not supplement it with chat history, event prose, worker output, filesystem scanning, repository probes, or ad hoc GitHub queries.
Use structured current state rather than interpreting the event timeline.
Keep a dependency- or time-gated queued item in Up Next until its gate is satisfied.
Do not turn an observation into an action from inside this skill.

Plain `/status` writes nothing.
Only an explicit file-output option on `/status` may request a replacement report artifact.
`TODO(spec-gap)`: V1 does not define the report sink or file contract, so until it does, explain that file mode is unavailable and do not write a file.

## Response contract

Render exactly these four sections in this order, each present even when empty:

1. **Needs Your Attention** - only a decision, review-ready PR, credential, permission, or blocker that requires the operator now.
   Empty state: “Nothing needs your attention right now.”
2. **Recently Completed** - the bounded recent successful task and delivery baseline from structured state.
   Empty state: “No recent completions are in the current snapshot.”
3. **Underway** - active work progressing without operator action, one concise line per task.
   Empty state: “Nothing is underway.”
4. **Up Next** - queued or gated work waiting on dependencies, capacity, or time and not on operator action.
   Empty state: “Nothing is queued.”

Put every item in exactly one section.
Working or validating tasks never belong in Needs Your Attention.
Resolved work never belongs in Underway.
Worker-session idleness is not task completion.
Include every PR as a complete `https://...` URL.
Keep secrets and sensitive values out of the response.
