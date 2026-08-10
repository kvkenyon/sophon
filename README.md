# Sophon

Sophon is single-operator, local orchestration for autonomous coding agents. One ordinary commander starts at an ordinary workspace root, sees its registered child projects, and coordinates project-confined workers without becoming bound to the repository that happened to launch it. Related work across projects is split into separate tasks and worktrees; no worker receives an unconstrained multi-repository checkout.

Sophon's only state model is a filesystem protocol under `~/.sophon/`. Typed immutable records are truth, and status is derived from those records plus live Herdr, Treehouse, Git, review, and forge observation. A narrow notification monitor and an opened Read the Code event bridge can improve latency, but neither carries truth, lifecycle authority, recovery, or commander ownership. There is no database or managed runtime.

> Reasoning may be probabilistic. Execution state may not be.

## Authority in plain language

The commander is the operator's planning interface, not an eager execution trigger.

- “What should we build?”, “decide what to build”, “explore”, “research”, “recommend”, “scope options”, and “talk it through” produce discussion only. They create no mission, task, lease, commit, worker, or monitor event.
- “Build this”, “implement the accepted proposal”, “start development”, or an explicit approval that clearly refers to a concrete proposal authorizes implementation.
- Mixed or ambiguous wording gets one concise confirmation before any effect. Scope-only permission is not permission to execute, and an unrelated conversational “yes” is not proposal approval.
- GitHub repository creation, adding a remote, pushing a branch, and opening a PR are separate later effects. None is a prerequisite or implicit result of local development.

The full behavioral contract is embedded in `prompts/commander/AGENTS.md` and rendered by `sophon prompt commander`.

## Install

```bash
go build -o "$HOME/.local/bin/sophon" ./cmd/sophon
```

Ensure `$HOME/.local/bin` is on `PATH`. The installed binary includes its Pi
presentation assets, so `sophon pi --workspace ROOT` needs neither a source
checkout beside it nor a globally installed Sophon Pi package.

External tools (`herdr`, `treehouse`, `git`, `gh-axi`) are resolved from PATH by default and can be overridden by command flags. Read the Code is separately configured with `--read-the-code PATH` or `SOPHON_READ_THE_CODE=PATH`; Sophon never downloads or installs it.

## Workspace-first quickstart

```bash
# Create an organization boundary, not a Git repository or managed commander.
sophon workspace init /work/product

# Direct children are the only project roots. New projects begin as empty,
# local Git repositories; existing work can be cloned or explicitly adopted.
sophon project create api --workspace /work/product
sophon project clone web --workspace /work/product --source /fixtures/web.git
sophon project list --workspace /work/product

# Preferred conversational entry: Sophon validates the workspace, launches
# an ordinary disposable Pi process at its root, and loads the commander
# prompt plus Sophon's Pi presentation extension. Pi model/reasoning options
# stay Pi's own options after `--`.
sophon pi --workspace /work/product -- --model anthropic/claude-sonnet-4-6 --thinking high

# Direct CLI setup remains available, including when using another commander.
cd /work/product
sophon prompt commander
sophon commander attach --scope /work/product

# Direct CLI equivalent after the operator has explicitly authorized a build.
sophon mission create --workspace /work/product --project api \
  --title "Local API prototype" --objective "Build and test the accepted API proposal"
sophon task create --mission <mission-id> --title "Build local API prototype" \
  --objective "Implement the accepted proposal with tests" \
  --delivery local --validate "go test ./..." --review required

# Task creation is durable planning only. It derives `planned` and a start
# action; it does not allocate a lease or claim a worker exists.
sophon status
sophon spawn <task-id>
```

For Pi extension development, pass its source explicitly without installing
or mutating Pi globally: `sophon pi --workspace /work/product --extension /path/to/integrations/pi/index.ts`.

When a local task starts in a genuinely empty initialized repository, `spawn` publishes typed bootstrap intent, creates exactly one deterministic empty initial commit on the repository's intended branch, records an exact receipt, and then allocates the worker. It never creates scaffolding, a README, license, ignore file, remote, public branch, or product file. Any untracked, ignored, symlinked, unusual, or ambiguous content is a refusal for an operator decision.

The normal lifecycle is:

```bash
sophon status                    # drain exact start/verify/validate/review actions
sophon verify-complete <task-id>
sophon validate <task-id>
sophon review open <task-id>     # when configured; approval is evidence only
sophon release <task-id>         # local branch/commit and records remain exact
sophon status --all              # complete immutable history
```

A local verified result is not delivery. After the operator separately creates/configures the remote, an immutable delivery selection may be recorded:

```bash
sophon delivery select <task-id> --mode pr \
  --title "Add the API prototype" --branch api/prototype --confirmed

# This is another authority boundary. Every push/PR effect still needs a
# fresh confirmation and the existing exact-head/public-surface checks.
sophon deliver <task-id> --confirmed
```

Creating the GitHub repository and adding `origin` is itself a distinct explicit action:

```bash
sophon project publish api --workspace /work/product \
  --repository OWNER/REPO --remote-url git@github.com:OWNER/REPO.git \
  --visibility private --confirmed
```

It is never invoked by project creation, bootstrap, spawn, review, validation, or delivery selection. Existing branch/PR tasks remain supported by selecting their delivery posture at task creation.

## Truth and recovery

- A newly created task is `planned`. Only a canonical spawn receipt makes it active. Allocation failure remains planned and retrying start reuses the same attempt.
- Empty-repository bootstrap, planned-task cancellation/replacement, local-to-public selection, delivery, release, and review evidence are typed immutable records. Intent precedes each corresponding external mutation; identical recovery converges and conflicting reality refuses.
- Workers write only attempt-scoped completion/report submissions. The CLI validates schema, identity, and live HEAD before publishing canonical evidence.
- Missing monitor or commander processes lose latency only. Completed work waits on disk and appears in derived status; no daemon advances or repairs it.
- `sophon commander attach --scope ROOT` stores volatile routing for the one workspace commander. Worker tabs from every child project join its registered Herdr workspace, while project identity remains pinned and rechecked against rename, replacement, symlink, and Git-identity drift.
- Required Read the Code approval is exact-head evidence. It neither delivers local work nor substitutes for `--confirmed`.
- Public delivery pushes an exact verified SHA only after current repository/branch/remote observation and public-content preflight. Sophon never merges.

## Documentation

- `docs/filesystem-protocol.md` — authoritative state, workspace, bootstrap, and lifecycle design.
- `docs/behavioral-contracts.md` — proposal/execution, commander, project-selection, and worker behavior.
- `docs/notification-monitor.md` — private optional JSON-RPC notification transport.
- `docs/read-the-code-review.md` — exact-revision local review and delivery gate.

Named for the all-seeing sophons in *The Three-Body Problem*.
