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
This procedure is an evidence gate, not a preference for a separate scout: keep
diagnosis inside a substantive implementation task when the operator already
authorized a fix and the uncertainty does not materially change what should be
built. Create a scout only when investigation is itself the requested
deliverable or its result controls a genuine implementation decision.

## Establish observed behavior

Start from the end user's experience rather than an internal error string or favored hypothesis.
Require an end-to-end reproduction aligned with the real path whenever feasible and safe.
If that is not possible, record the exact limitation and do not present a representative path as equivalent evidence.
Capture expected behavior, observed behavior, setup, inputs, relevant versions
or state, frequency, and repeatability before assigning cause. Establish a
baseline before changing conditions. Preserve the smallest faithful reproducer
that still crosses the real behavioral boundary; a unit-level reproduction is
not interchangeable with the user path unless the report proves the boundary
is equivalent.

Separate three facts explicitly:

- The **initiating trigger** starts the faulty behavior.
- The **masking condition** independently hides or exposes the fault.
- The **visible symptom** is what the user or operator observes.

Do not collapse them into one label.
A masking condition may explain intermittency without causing the fault. The
visible symptom may be several layers downstream from both. Require the report
to say which evidence establishes each role rather than naming one nearby fact
as "the root cause."

## Test the causal explanation

Compare the failing path with a proven path where intended behavior works.
Find their earliest meaningful divergence across inputs, state transitions, dependencies, timing, and control flow.
Inspect relevant history, including blame, commits, migrations, and prior
implementations, when it can explain intended invariants or the divergence.
Do not treat the nearest recent change as causal without evidence.

Identify the smallest counterfactual that should change the result if the leading explanation is true.
Change one condition at a time where practical and record the outcome.
Name evidence that would falsify the explanation, run that check when feasible, and retain contradictory results.
The final explanation must account for both the failure and proven success path.

Compare plausible alternatives explicitly. Prefer the explanation that covers
all observed cases with the fewest unsupported assumptions, but do not turn
simplicity or confidence into proof. If timing, cache, environment, concurrency,
or configuration is implicated, vary it independently from the initiating
trigger. Repeat nondeterministic tests enough to distinguish a causal change
from noise, and state the remaining statistical limitation.

## Evaluate the report

Before accepting a diagnosis, verify that the evidence chain answers all of
these questions:

1. What user-visible contract is violated, and how was that violation observed?
2. What exact trigger initiates it, what condition masks or exposes it, and what
   symptom finally appears?
3. Where is the earliest proven divergence from a working path?
4. Which counterfactual changed the outcome as predicted?
5. Which plausible alternatives were tested, and what would still falsify the
   leading explanation?
6. Does the proposed causal boundary explain both failure and success without
   relying on an untested masking condition?
7. What uncertainty remains, and could it materially alter fix scope or risk?

Do not accept a report that merely finds correlated code, reproduces only an
internal error, cites a recent diff, or proposes a plausible patch. Send a
load-bearing evidence gap back to the same substantive task when its accepted
outcome remains valid. Commission another scout only when the missing question
is itself a coherent investigation deliverable, not a crumb-sized evidence
chore.

## Scope the result

A diagnostic task brief should require the reproduction, trigger-mask-symptom
separation, failing and proven path comparison, relevant history, alternative
causes, smallest counterfactual, disconfirming evidence, and remaining
uncertainty.
Distinguish observed facts from hypotheses.
Scope the recommended change to the smallest causal boundary supported by the
evidence, not automatically the smallest textual edit. Include every test and
documentation correction necessary to prove and preserve accepted behavior,
but do not generalize the fix to unobserved cases without authority.

If a load-bearing element is missing, keep the result provisional and obtain
that evidence rather than treating confidence, implementation detail, or a
passing narrow test as proof.

A diagnosis or implementation-ready recommendation is evidence, not authorization to change code.
When implementation is authorized, carry the faithful reproduction into
behavioral regression coverage whenever possible. Validate the fix against the
original failure, the proven success path, and the disconfirming case so a patch
that merely hides the symptom cannot pass as a causal correction.
