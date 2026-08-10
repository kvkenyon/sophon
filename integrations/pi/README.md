# Sophon Pi presentation

`integrations/pi/index.ts` is Sophon's directly loadable Pi presentation extension. It is intentionally independent of launcher code:

```sh
SOPHON_PI=1 \
SOPHON_DATA_HOME=/absolute/path/to/data-home \
pi -e /absolute/path/to/sophon/integrations/pi/index.ts
```

Pi loads the TypeScript entry point itself; this package has no install or build step. The extension targets the supported APIs shipped by Pi 0.84.1 and uses `createBashToolDefinition` only after probing that the exact call, result, and execution capabilities are present.

## Presentation contract

- `/calm` toggles Calm immediately and writes the preference atomically to `<SOPHON_DATA_HOME>/pi/presentation.json`. Without `SOPHON_DATA_HOME`, the same resolution as Sophon is used: `~/.sophon`.
- Calm is display-only. User prompts and assistant answers are unchanged. Markdown transformation suppresses displayed thinking blocks without changing stored messages or model context, so `/export` and `/share` retain Pi's ordinary content.
- Successful, collapsed Bash tool rows whose executable is `sophon` become a single restrained command label. Expanding the row restores stock output. Failures and output that signals attention, conflicting or invalid evidence, warnings, preserved work, decisions, refusals, or delivery confirmation use Pi's stock renderer.
- Turning Calm off restores Pi's normal thinking label, working indicator, and tool rendering in the same session.
- In print, JSON, and RPC modes, terminal-only presentation is skipped. The preference and command remain safe; no timer, overlay, or background resource is started.

The Bash integration delegates execution to Pi's current built-in definition for the active working directory. Pi's stock result renderer is still allowed to finalize its internal elapsed-time state before a successful result is visually compacted. If that exact renderer contract is absent in another Pi build, only Sophon row compaction is disabled and a TUI warning is shown; execution is never replaced with an approximation.

## Sophon-launched opening

`SOPHON_PI=1` enables the opening screen once at interactive process startup. It is a full-width overlay with original terminal artwork: three independently orbiting suns, a chaotic sophon trace, sparse observation/interference marks, branding, and sanitized workspace/project names when available. Enter closes it.

The launcher may optionally set `SOPHON_WORKSPACE_NAME` and `SOPHON_PROJECT_NAME`. Otherwise, safe basename-only context is derived from Pi's working directory. Narrow terminals use a static layout. Animation timers are unreferenced and disposed on Enter, overlay disposal, or session shutdown.

During a logical agent run, Calm uses Pi's own working-indicator animation facility for a seven-column Three-Body field. It starts on `agent_start` and returns to Pi's default on `agent_settled`, so Pi owns repaint timing and resize behavior.

## Tests

```sh
cd integrations/pi
npm test
```

The tests use Node's built-in TypeScript stripping and test runner. They cover persistence and data-home resolution; Calm on/off rendering; visible failures and operational warnings; unchanged export content; splash input, resize, animation, and disposal; working animation lifecycle; and non-TUI degradation.
