---
name: diagnostic-reasoning
description: Agent-only procedure for scoping reported bugs and evaluating diagnostic reports through reproduction, causal separation, counterfactual testing, and disconfirming evidence.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/diagnostic-reasoning/SKILL.md. -->

# Diagnostic reasoning

Use this procedure before scoping a reported bug and before acting on a diagnostic report.
The commander applies it when briefing a scout or implementation task and evaluating the result without taking over repository investigation.

## Establish observed behavior

Start from the end user's experience rather than an internal error string or favored hypothesis.
Require an end-to-end reproduction aligned with the real path whenever feasible and safe.
If that is not possible, record the exact limitation and do not present a representative path as equivalent evidence.
Capture expected behavior, observed behavior, setup, inputs, and repeatability before assigning cause.

Separate three facts explicitly:

- The **initiating trigger** starts the faulty behavior.
- The **masking condition** independently hides or exposes the fault.
- The **visible symptom** is what the user or operator observes.

Do not collapse them into one label.

## Test the causal explanation

Compare the failing path with a proven path where intended behavior works.
Find their earliest meaningful divergence across inputs, state transitions, dependencies, timing, and control flow.
Inspect relevant history when it can explain intended invariants or the divergence.
Do not treat the nearest recent change as causal without evidence.

Identify the smallest counterfactual that should change the result if the leading explanation is true.
Change one condition at a time where practical and record the outcome.
Name evidence that would falsify the explanation, run that check when feasible, and retain contradictory results.
The final explanation must account for both the failure and proven success path.

## Scope the result

A diagnostic task brief should require the reproduction, trigger-mask-symptom separation, failing and proven path comparison, relevant history, smallest counterfactual, disconfirming evidence, and remaining uncertainty.
Distinguish observed facts from hypotheses.
If a load-bearing element is missing, create a focused follow-up scout rather than treating confidence or implementation detail as proof.

A diagnosis or implementation-ready recommendation is evidence, not authorization to change code.
When implementation is separately authorized, carry the reproduction into behavioral regression coverage whenever possible.
