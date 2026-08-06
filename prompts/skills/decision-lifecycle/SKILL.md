---
name: decision-lifecycle
description: Agent-only lifecycle for preserving unresolved operator choices discovered in scout reports, reviews, validation, and worker blockers as durable Signals.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/decision-hold-lifecycle/SKILL.md. -->

# Decision lifecycle

Use this skill before treating a scout, review, validation result, worker blocker, or structured report with possible operator choices as fully handled, and when routing the operator's answer.

## Policy

Every genuine unresolved choice that belongs to the operator must become a durable Signal in the originating mission before the originating activity is considered completely handled.
The commander performs the semantic inventory because the control plane must not infer decisions from report prose.
Give each distinct choice a stable, privacy-safe identity and use `await intellect.create_signal(...)` idempotently so retries reuse the same Signal while different choices remain distinct.

Do not create a Signal for a resolved finding, a recommendation that needs no operator choice, or prose that merely sounds decision-like.
Do not close a Signal merely because its report is complete, its review ended, or its worker became inactive.
The Signal remains open until the answer is durably recorded and its dependent tasks remain correctly controlled.
Current status reads structured Signal state and must not compensate by scraping historical prose.

## Operating sequence

1. Read the complete originating artifact and include every relevant review result.
2. Inventory only genuine unresolved operator choices.
3. For each choice, create or reuse a Signal with its mission, optional task, kind, question, context, options, and recommendation.
4. Verify that the full inventory is registered before treating the originating activity as handled.
5. Notify the operator in outcome language with evidence, consequences, and a recommendation.
6. Keep dependent tasks blocked or unstarted through structured dependencies while the Signal is open.
7. After the operator answers, record the answer with `await intellect.resolve_signal(...)` and route it to affected workers through `await intellect.message_worker(...)` only when their current tasks remain valid.
8. Confirm that the Signal is resolved in structured status and dependent work remains durably represented.

Never let chat alone become the decision record.
Never let the originating worker answer its own Signal.
