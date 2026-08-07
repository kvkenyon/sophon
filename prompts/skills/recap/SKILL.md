---
name: recap
description: Session-only recap for /recap or an explicit request to summarize what happened since the operator's previous real message; it never gathers fresh task state.
user-invocable: true
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/ahoy/SKILL.md. -->

# Recap

Give the operator a concise session-only recap without gathering fresh mission state.

1. Inspect only conversation and session history already visible to the commander.
2. Find the most recent real operator-authored message before the current `/recap` invocation.
   Use trusted message provenance when available.
   Platform, system, developer, tool, control-plane, and other injected operational messages are not operator messages merely because a transcript renders them with a user-like role.
   Do not exclude an ordinary operator message merely because it quotes operational content.
   `TODO(spec-gap)`: V1 does not define a portable origin marker for synthetic user-role messages, so do not invent prefix matching.
3. If no prior real operator message exists, say that no earlier operator-visible session interval is available to recap.
4. Otherwise summarize what happened after that prior operator message and before the current invocation.
   Include concrete outcomes, delivered work, failures, decisions made, newly needed decisions, and work still running only when visible in that interval.
   Preserve every full PR URL visible in the interval.
5. Inspect the whole visible session before the invocation for explicit operator decisions that are still visibly unanswered, including choices raised before the recap boundary.
   A later unrelated message does not resolve an earlier choice.
   Treat it as closed only when a later visible response directly answers, declines, approves, or rejects it.
   Include each supported open decision once and deduplicate by substance.
6. Do not query Sophon, the filesystem, GitHub, a browser, or any other external source in the normal recap branch.
   Create no artifact and mutate no state.
   Do not guess live state beyond the last visible event.
7. If nothing occurred after the boundary but an older visibly open decision exists, report that decision.
   If neither exists, say in one sentence that nothing happened after the previous operator message.

The current invocation is outside the recap interval.
A prior `/recap` is a real operator boundary.
If context compaction hides the boundary, state that limitation and summarize only visibly supported events.
Do not invoke `status`; `/recap` is session-only even at first contact.
