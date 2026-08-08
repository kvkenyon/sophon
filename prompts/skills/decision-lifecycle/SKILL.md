---
name: decision-lifecycle
description: Agent-only lifecycle for inventorying unresolved operator choices in worker results and routing the operator's answer into durable task records.
user-invocable: false
metadata:
  internal: true
---

# Decision lifecycle

Use this skill before treating a worker result, validation failure, or completion review with possible operator choices as fully handled, and when routing the operator's answer.

## Policy

Every genuine unresolved choice that belongs to the operator must be surfaced before the originating task is considered completely handled.
The commander performs the semantic inventory: read the entire attempt result — summary, risks, changed files — not only the worker's headline, and identify each distinct choice.

Do not escalate a resolved finding, a recommendation that needs no operator choice, an ordinary implementation detail, or prose that merely sounds decision-like.
A task is not fully handled while a genuine choice it surfaced remains unanswered, even when its result is published and verified.

## Operating sequence

1. Read the complete attempt result and the verified evidence.
2. Inventory only genuine unresolved operator choices; apply the `operator-authority` test to each.
3. Raise each distinct choice to the operator in one concise, self-contained message (accepted requirement, evidence and conflict, smallest compliant alternative, consequences, recommendation).
4. Keep dependent work unstarted — queued, not spawned — while the choice is open.
5. When the operator answers, make the answer durable before work continues:
   - an answer that changes accepted scope becomes part of the objective of the new substantive task you create for it;
   - an answer that keeps the live task valid is sent to the worker once, exactly, through `sophon send`, naming the decision and the required outcome;
   - an answer that invalidates live work settles the current attempt's custody first, then a replacement attempt carries the decision in its new context.
6. Confirm through `sophon status` that dependent work remains accurately represented and that the next attempt's brief reflects the answer.

Chat alone is not the decision record.
The answer must be visible in durable intent — a task objective — or in a recorded steering message tied to a still-valid attempt before the originating task is treated as fully handled.
Never let the originating worker answer its own blocker, and never re-raise an answered choice because a commander session restarted: reconstruct what was already decided from durable records and the conversation's explicit answers.
