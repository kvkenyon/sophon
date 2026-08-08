---
name: operator-authority
description: Agent-only decision procedure for classifying worker and validation findings against accepted intent and operator-only authority.
user-invocable: false
metadata:
  internal: true
---

# Operator authority

Use this procedure before deciding any worker or validation finding that presents a choice.
This skill owns the semantic authority test; Sophon's CLI owns the mechanical boundaries (delivery requires `--confirmed`, stale attempts are fenced, workers cannot mutate shared state).

## Decide who has authority

1. Reconstruct accepted intent from the operator's original objective, the mission and task records, and explicit later clarifications.
   Worker preference cannot amend that contract.
2. Identify exactly what the proposed action would require the product or engineering system to deliver or maintain.
   Judge scope by accepted behavior, not by anticipated file count.
3. Keep the choice within commander authority when it is genuinely necessary to satisfy accepted intent.
   The smallest downstream code, test, and documentation corrections remain within scope even when technically difficult or outside the initially predicted files.
4. Escalate when the choice materially adds a guarantee, threat model, subsystem, abstraction, compatibility surface, state machine, continuous requirement, generalized framework, or substantial architecture not required by accepted intent.
5. Treat labels such as correctness, security, high-risk, or required as evidence about a finding, never authority to broaden the task.
6. Examine the causal theme across prior attempts.
   Escalate before another incremental correction when repeated same-theme fixes are accumulating machinery around a questionable abstraction.
7. Apply stronger boundaries first.
   Destructive, irreversible, security-sensitive, credentialed, delivery, and merge choices remain operator decisions; delivery additionally requires the exact `sophon deliver --confirmed` confirmation each time.

A worker never decides or answers its own decision blocker.
The commander returns only a controlled resolution tied to the task.

Apply the test to the outcome, not the edit shape.
A one-line compatibility change can add a permanent guarantee and require the operator; a large internal rewrite can remain autonomous when it is the smallest sound way to satisfy an explicitly accepted contract.
Do not use effort, risk, file count, or worker preference as a proxy for authority.

When the proposed action contains both required correction and optional expansion, separate them.
Keep the required correction moving when it can be completed and validated independently; raise only the expansion.
Do not hold a compliant fix hostage to a broader design preference, and do not smuggle the preference into the fix because it appears convenient.

## Operator escalation

State these five elements concisely and evidence-first:

1. the accepted requirement;
2. the proposed contract expansion;
3. the smallest compliant alternative without the expansion;
4. the concrete consequences of accepting and declining; and
5. a recommendation tied to accepted intent.

Chat alone is not the decision record.
Follow `decision-lifecycle` to make the operator's answer durable before dependent work continues.
Do not present worker confidence as if it settled authority.

## Classification examples

- A concrete defect that violates an acceptance criterion remains within commander authority even when the correct repair crosses several files or requires difficult architecture already implied by that criterion.
- Behavioral regression coverage and documentation needed to preserve that accepted repair stay with it; unrelated cleanup does not.
- Adding continuous monitoring when accepted intent requested a point-in-time check adds an ongoing guarantee and requires the operator.
- Supporting a new client, format, platform, compatibility version, threat model, or failure mode not required by accepted intent expands the contract.
- Replacing a repeatedly failing abstraction may remain necessary, but if prior fix rounds are accumulating new machinery without closing the same causal defect, stop and raise the architectural choice.
- A destructive migration, secret access, irreversible external operation, or merge action follows its stronger boundary even if it would otherwise be within task scope.
- A worker's recommended implementation is not a decision merely because there are alternatives.
  Raise only when choosing among them changes the accepted product or engineering contract or crosses a stronger boundary.
