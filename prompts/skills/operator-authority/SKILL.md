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

Apply the test to the outcome, not the edit shape. A one-line compatibility
change can add a permanent guarantee and require the operator; a large internal
rewrite can remain autonomous when it is the smallest sound way to satisfy an
explicitly accepted contract. Do not use effort, risk, file count, worker
preference, or review severity as a proxy for authority.

When the proposed action contains both required correction and optional
expansion, separate them. Keep the required correction moving when it can be
completed and validated independently; raise only the expansion. Do not hold a
compliant fix hostage to a broader design preference, and do not smuggle the
preference into the fix because it appears convenient.

## Operator escalation

State these five elements concisely and evidence-first:

1. the accepted requirement;
2. the proposed contract expansion;
3. the smallest compliant alternative without the expansion;
4. the concrete consequences of accepting and declining; and
5. a recommendation tied to accepted intent.

If operator authority is required, create or reuse the durable Signal with `sophon signal raise --mission ID --task ID --kind decision --question TEXT --context TEXT --recommendation TEXT --command-id ID --json` rather than leaving the choice only in chat. Inspect a reused Signal with `sophon signal inspect ID --json`.
Do not present a review label or model confidence as if it settled authority.

## Classification examples

- A concrete defect that violates an acceptance criterion remains within
  commander authority even when the correct repair crosses several files or
  requires difficult architecture already implied by that criterion.
- Behavioral regression coverage and documentation needed to preserve that
  accepted repair stay with it; unrelated cleanup does not.
- Adding continuous monitoring when accepted intent requested a point-in-time
  check adds an ongoing guarantee and requires the operator.
- Supporting a new client, format, platform, compatibility version, threat
  model, or failure mode not required by accepted intent expands the contract.
- Replacing a repeatedly failing abstraction may remain necessary, but if prior
  fix rounds are accumulating new machinery without closing the same causal
  defect, stop and raise the architectural choice.
- A destructive migration, secret access, irreversible external operation, or
  future merge action follows its stronger boundary even if it would otherwise
  be within task scope.
- A worker's recommended implementation is not a decision merely because there
  are alternatives. Raise only when choosing among them changes the accepted
  product or engineering contract or crosses a stronger boundary.
