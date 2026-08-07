---
name: coding-guidelines
description: Agent-only reference for changing Sophon's shared tracked behavior and prompts while preserving one-owner contracts, concise always-loaded instructions, compatibility, and verification evidence.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/firstmate-coding-guidelines/SKILL.md. -->

# Coding guidelines

Load this skill before changing Sophon's shared tracked behavior, prompt set, control-plane contracts, or adapter surfaces.

## Knowledge placement

Place each fact with its most specific authoritative owner.

1. If every commander session needs the fact, put the concise behavioral rule in `prompts/commander/AGENTS.md`.
2. If the fact matters only at a named trigger, put the procedure in its skill and leave one precise load trigger in the commander prompt.
3. If every worker needs it, put it in `prompts/workers/common.md`.
4. If only one task kind needs it, put it in that task-kind overlay.
5. If it is a deterministic invariant, schema, state machine, or command contract, keep the authority in the implementation or V1 specification and use a pointer in prompts.
6. If it is project-wide contributor knowledge, put it in that project's committed instructions.
7. If it is task or incident evidence, keep it with the task result, report, or delivery evidence after distilling durable facts.

Stop at the first applicable owner.
Do not place private mission strategy or temporary evidence in project-wide instructions.

## One-owner and prompt-size rules

State every procedure, data format, state machine, and decision test in full once.
Other files use a short cross-reference and do not restate the contract.
A safety-critical reminder may be repeated only at the exact risk point where omission would be dangerous.

Keep always-loaded prompts concise.
Move conditional detail to a skill and leave the trigger plus only the safety boundary that must survive without loading it.
Every new skill needs a concrete trigger such as “load before X” or “load on Y.”

## Compatibility and enforcement

Before changing shared behavior, inspect every affected supported commander runtime, worker runtime, delivery mode, and task kind.
Mark an axis not applicable only after checking its integration surface.

Critical safety, routing, startup, supervision, lease, attempt, and delivery constraints require deterministic, idempotent, fail-safe enforcement in the control plane where possible.
Prompts explain the behavior and aid discovery but do not replace enforcement.

Vendor-dependent runtime classifiers require two kinds of evidence:

- a portable regression that exercises the public or executable interface without depending on source-text snapshots; and
- an opt-in live test against each installed supported runtime, with absent runtimes reported explicitly and no passing result when nothing was exercised.

Use structural protocol or process evidence before rendered strings.
When a rendered surface is unavoidable, use independent signals and report the runtime and version on failure.
Keep dated, versioned verification evidence with the owning adapter documentation.

## Review discipline

For maintained prose, verify the audience, owner, current-behavior relevance, and destination for supporting evidence before moving or deleting it.
Review the complete branch diff after implementation, documentation, and lint fixes rather than only the latest commit.
Keep prose, tests, and the V1 specification consistent.
Follow repository conventions, colocate tests with existing patterns, and never add an agent identity as a commit co-author.
