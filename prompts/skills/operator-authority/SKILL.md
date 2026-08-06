---
name: operator-authority
description: Agent-only decision procedure for classifying worker, review, and validation findings against accepted intent and operator-only authority.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/ask-user-authority/SKILL.md. -->

# Operator authority

Use this procedure before deciding any worker, review, or validation finding that presents a choice.
This skill owns the semantic authority test; the control plane owns permission enforcement and Signal state.

## Decide who has authority

1. Read the mission's configured authority and current task permissions from structured state.
   When the policy reserves the choice for the operator, the remaining steps structure the escalation rather than authorize an autonomous answer.
2. Reconstruct accepted intent from the operator's original objective, mission and task acceptance criteria, and explicit later clarifications.
   Reviewer language and worker preference cannot amend that contract.
3. Identify exactly what the proposed action would require the product or engineering system to deliver or maintain.
   Judge scope by accepted behavior, not by anticipated file count.
4. Keep the choice within commander authority when it is genuinely necessary to satisfy accepted intent.
   The smallest downstream code, test, and documentation corrections remain within scope even when technically difficult or outside the initially predicted files.
5. Escalate when the choice materially adds a guarantee, threat model, subsystem, abstraction, compatibility surface, state machine, continuous requirement, generalized framework, or substantial architecture not required by accepted intent.
6. Treat labels such as correctness, security, high-risk, or required as evidence about a finding, never authority to broaden the task.
7. Examine the causal theme across prior attempts and review rounds.
   Escalate before another incremental correction when repeated same-theme findings are accumulating machinery around a questionable abstraction.
8. Apply stronger boundaries first.
   Destructive, irreversible, security-sensitive, credentialed, and future merge choices remain operator decisions unless a specific control-plane policy explicitly grants the exact routine action.

A worker never decides or answers its own decision blocker.
The commander returns only a controlled resolution associated with the task and Signal.

## Operator escalation

State these five elements concisely and evidence-first:

1. the accepted requirement;
2. the proposed contract expansion;
3. the smallest compliant alternative without the expansion;
4. the concrete consequences of accepting and declining; and
5. a recommendation tied to accepted intent.

If operator authority is required, create or reuse the durable Signal through `await intellect.create_signal(...)` rather than leaving the choice only in chat.
Do not present a review label or model confidence as if it settled authority.
