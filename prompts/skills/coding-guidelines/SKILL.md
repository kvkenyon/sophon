---
name: coding-guidelines
description: Agent-only reference for changing Sophon's shared tracked behavior and prompts while preserving one-owner contracts, concise always-loaded instructions, compatibility, and verification evidence.
user-invocable: false
metadata:
  internal: true
---

# Coding guidelines

Load this skill before changing Sophon's shared tracked behavior, prompt set, filesystem-protocol contracts, or CLI surface.

## Knowledge placement

Place each fact with its most specific authoritative owner.

1. If every commander session needs the fact, put the concise behavioral rule in `prompts/commander/AGENTS.md`.
2. If the fact matters only at a named trigger, put the procedure in its skill and leave one precise load trigger in the commander prompt.
3. If every worker needs it, put it in `prompts/workers/common.md`.
4. If only the implementation overlay needs it, put it in `prompts/workers/implementation.md`.
5. If it is a deterministic invariant, record schema, derivation rule, or command contract, keep the authority in `docs/filesystem-protocol.md` and the implementing package (`internal/store`, `internal/flow`) and use a pointer in prompts.
6. If it is project-wide contributor knowledge, put it in that project's committed instructions.
7. If it is task or incident evidence, keep it with the attempt's result or delivery records after distilling durable facts.

Stop at the first applicable owner.
Do not place private mission strategy or temporary evidence in project-wide instructions.

## One-owner and prompt-size rules

State every procedure, data format, and decision test in full once.
Other files use a short cross-reference and do not restate the contract.
A safety-critical reminder may be repeated only at the exact risk point where omission would be dangerous.

Keep always-loaded prompts concise.
Move conditional detail to a skill and leave the trigger plus only the safety boundary that must survive without loading it.
Every new skill needs a concrete trigger such as "load before X" or "load on Y."

## Compatibility and enforcement

Before changing shared behavior, inspect every affected surface: the commander prompt, the worker overlays, both delivery modes, and the CLI contract in `docs/filesystem-protocol.md`.
Mark an axis not applicable only after checking its integration surface.

Critical safety, lease, attempt-fencing, and delivery constraints require deterministic, idempotent, fail-safe enforcement in the command core where possible.
Prompts explain the behavior and aid discovery but do not replace enforcement.

Runtime classifiers require two kinds of evidence:

- a portable regression that exercises the public or executable interface without depending on source-text snapshots; and
- an opt-in live test against the installed runtime, with an absent runtime reported explicitly and no passing result when nothing was exercised.

Use structural protocol or process evidence before rendered strings.
When a rendered surface is unavoidable, use independent signals and report the runtime and version on failure.

## Review discipline

For maintained prose, verify the audience, owner, current-behavior relevance, and destination for supporting evidence before moving or deleting it.
Review the complete branch diff after implementation, documentation, and lint fixes rather than only the latest commit.
Keep prose, tests, and `docs/filesystem-protocol.md` consistent.
Follow repository conventions, colocate tests with existing patterns, and never add an agent identity as a commit co-author.
