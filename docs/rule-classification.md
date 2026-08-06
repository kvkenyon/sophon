# FirstMate rule classification

This is the Milestone 0 classification of the imported baseline at [`prompts/upstream/firstmate/`](../prompts/upstream/firstmate/).
“Both” means the control plane must reject or prevent an invalid state and the derived captain/worker prompt must explain the corresponding operating policy.
Mechanism names in the vendored text (`fm-*` scripts, `FM_HOME`, tmux, files, watcher hooks, and no-mistakes subcommands) are not V1 mechanisms.
For each such rule, the classification below applies to its behavioral constraint, not to copying its shell implementation.

## Always-loaded baseline

| Source | Imported rules, including all rules in the cited section | Class |
|---|---|---|
| `AGENTS.md` preamble | Address the human, optional nautical tone, and communicate bad news plainly. | Behavioral policy |
| §1 identity, hard rule 1 | The coordinator does not perform project changes except within narrowly authorized operations; it must not force, stash, discard, or broaden an authorization. | Both |
| §1 hard rules 2–3 | No merge without explicit human authority; preserve all unlanded work; only discard under explicit authority and only discard declared scratch after its evidence/decision gate. | Both |
| §1 hard rules 4–5 | Workers never address the human directly; report outcomes faithfully, including failure evidence. | Both |
| §1 shared/private material | Separate shared instructions from private operational state; avoid competing with live workers; do not add agent co-authors; send tracked changes through the selected delivery authority. | Both |
| §2 layout and state | Maintain authoritative separation among durable records, volatile events, configuration, task briefs, reports, metadata, and private state; an event line is not current state; use curated durable knowledge rather than conversational memory. | Both |
| §2 isolation facts | An explicit home/mission identity is required for a steer; do not silently resolve work against another home; project copies are read-only to the coordinator. | Both |
| §3 session start | Reconcile once from a bounded authoritative startup digest; acquire/verify exclusive coordinator ownership before mutation; lock refusal is read-only; do not repeat broad reads unless needed. | Both |
| §3 bootstrap | Detect before installing; obtain current operator consent before installation; do not dispatch without required tools and authentication; use the supported GitHub/browser/report interfaces. | Both |
| §4 adapter dispatch | Load a verified adapter before lifecycle operations; never launch an unverified adapter; malformed configuration is actionable rather than silently bypassed; unsupported dependency/auth/backend is a blocker. | Both |
| §4 profile selection | Apply explicit override, then matching rule, default, then static adapter; account for every candidate and disclose quota/auth uncertainty; do not silently downgrade reasoning quality or bias an evidence tie. | Behavioral policy |
| §5 recovery | Reconcile recorded task state and live inventory before new work; recover only direct reports; preserve work and identity; restart is not authority to invent work; surface only material decisions, review-ready work, failures, and credentials. | Both |
| §6 project and knowledge | Use the project-management procedure; route durable knowledge to its specific owner; project-wide knowledge belongs in project instructions; task findings belong with the task/report; do not put private strategy in project memory. | Behavioral policy |
| §7 intake: project and routing | Resolve each request to one project with a concise question when ambiguous; use the simplest direct path; do not build machinery without a concrete need; consult prior evidence before commissioning investigation. | Behavioral policy |
| §7 intake: task shape | Ship is the default implementation task; scout is a knowledge task; informational evidence is not change authority; do not launch speculative duplicate research; diagnose before scoping a reported bug. | Behavioral policy |
| §7 intake: delivery and concurrency | Persist selected delivery mode and authority at intake; use stated rigor; classify uncertain surface conservatively; overlap alone does not serialize work, while true semantic/external dependencies do. | Both |
| §7 dispatch | Spawn only through the isolated-task allocation path; stop if isolation fails; use concise bounded steers; record work under way and supervise it. | Both |
| §7 selected delivery path | The selected delivery path owns its rigor; do not add shadow manual gates; no-mistakes/direct-PR/local-only have distinct outcomes; workers do not decide their own ask-user findings; never merge red work; only guarded merge paths land work. | Both |
| §7 validation | A validation run owns its branch until its supported completion/abort sequence; use current code-matched structured state, not terminal liveness; route new requirements to follow-up unless accepted intent is invalidated; report green PRs promptly. | Both |
| §7 PR and teardown | Require structured PR-ready evidence, retain full URL evidence, register authenticated checks, tear down only after landing, treat teardown refusal as an investigation, retain bounded completion history. | Both |
| §7 scout promotion | A scout requires a self-contained report and unresolved-decision gate before completion; recommendations do not authorize implementation; promote through a controlled transition and retain only intentional changes/regression evidence. | Both |
| §8 supervision | Exactly one healthy supervision cycle; drain durable signals before action; events are not current state; recover stale workers through the recovery policy; no-change polls are silent; never broadly kill shared supervision. | Both |
| §8 away mode | Away mode has one owner, marked injections are internal, unmarked return is authoritative, and away mode does not expand human-approval boundaries. | Both |
| §9 escalation etiquette | Translate mechanisms into project outcome, consequence, evidence, and next decision; omit internal IDs/implementation jargon; preserve plain failure reporting, full PR URLs where relevant, and concise actionable prose. | Behavioral policy |
| §10 backlog contract | Backlog items, dependencies, decision records, status events, and completion state have one authoritative owner; do not infer current state from append-only events or create duplicate work. | Both |
| §11 briefs | Every worker receives a bounded, task-specific brief with scope, authority, worktree, validation/delivery expectations, status protocol, and prohibited actions. | Both |
| §12 self-update | Treat self-update as a controlled delivery operation, preserve unlanded work, and do not broaden its authority. | Both |
| §13 agent-only skills | Declare concrete load triggers; do not duplicate skill procedures in always-loaded instructions; preserve a clear authoritative owner for each procedure. | Behavioral policy |
| §14 X mode | External/public interaction is opt-in, routed through controlled state, and cannot grant extra approval authority or expose private operational details. | Both |
| captain-instruction precedence | A current explicit operator instruction overrides a conflicting standing instruction only within its concrete scope; stronger safety, destructive, security, and merge boundaries remain independent. | Both |
| maintaining this file | Keep shared instructions concise, route conditional detail to an owned skill/document, preserve load triggers, review cross-adapter effects, use deterministic/idempotent enforcement for critical infrastructure, and maintain executable behavioral tests and current evidence. | Both |

## Ported skill rules

| Source rule | Classification | V1 interpretation |
|---|---|---|
| `skills/ahoy` steps 1–7 | Behavioral policy | A recap uses only visible session history, distinguishes real operator messages from injections, falls back to status only at first contact, reports supported open decisions once, and never invents live state or mutates state. |
| `skills/bearings` opening and invocation modes | Both | Status is a fresh bounded snapshot; plain status is read-only, explicit file mode alone may create its report, and status must not trigger cleanup, dispatch, merge, steering, or decisions. |
| `skills/bearings` “What it does” | Both | One deterministic control-plane query owns the snapshot; structured state beats prose/events; gates remain gated until satisfied; the optional report is a complete replacement snapshot. |
| `skills/bearings` chat-response contract | Behavioral policy | Render Captain's Call, Recently Landed, Underway, and Charted Next in that order, always including empty states; place each item in exactly one bucket and keep action-free work out of operator action. |
| `skills/bearings` tone/supervision discipline | Behavioral policy | Keep status concise, safe, and free of secrets; do not turn an observation into an action. |
| `skills/ask-user-authority` “Decide who has authority” steps 1–8 | Both | Reconstruct accepted intent, distinguish downstream correctness from material contract expansion, escalate stronger destructive/irreversible/security choices, and prevent a worker from answering its own finding. |
| `skills/ask-user-authority` escalation and examples | Behavioral policy | An operator escalation states the accepted requirement, proposed expansion, smallest compliant alternative, consequences, and recommendation; labels such as “security” are evidence, not authority. |
| `skills/decision-hold-lifecycle` policy | Both | Every genuine unresolved operator choice found in a report/review becomes a unique durable signal before completion; idempotently register all keys; completion attests the full inventory; resolution records the answer and unblocks dependent tasks before closure. |
| `skills/decision-hold-lifecycle` operating sequence | Both | Read the complete artifact, inventory only genuine choices, notify the operator in outcome language, then confirm closure disappears from structured status while dependent work remains controlled. |
| `skills/diagnostic-reasoning` §§“Establish”, “Test”, “Scope” | Behavioral policy | First establish observed behavior and a reproducible baseline, test the causal explanation against alternatives, then recommend/implement the smallest evidence-supported change with regression evidence; a diagnosis is not change authority. |
| `skills/firstmate-coding-guidelines` knowledge placement and one-owner rules | Behavioral policy | Put information with its most specific authoritative owner, preserve one procedure owner, and use a short trigger instead of copying conditional procedures into a global prompt. |
| `skills/firstmate-coding-guidelines` inline stub, size, trigger hygiene | Behavioral policy | Keep derived prompts short and conditional detail in skills/docs; every skill has an explicit load trigger; write pointers, not duplicated rules. |
| `skills/firstmate-coding-guidelines` compatibility/enforcement | Both | Review every supported agent/runtime surface affected by a behavior change; use deterministic, idempotent, fail-closed enforcement for safety/routing/startup/supervision constraints. |
| `skills/firstmate-coding-guidelines` harness-dependent checks | Both | Vendor-dependent classifiers require real end-to-end evidence, portable regressions, opt-in live verification, explicit absent-harness reporting, and dated/versioned verification records. |
| `skills/firstmate-coding-guidelines` documentation/repo style | Behavioral policy | Review documentation for owner/audience/currentness before removal; inspect complete diffs; keep prose and tests consistent with repository conventions and evidence current. |
| `skills/project-management` preconditions/registry | Both | Projects have authoritative identity/registry metadata; resolve destination, delivery, and authority before mutation; never overwrite an existing project; safe rollback touches only newly created artifacts. |
| `skills/project-management` delivery posture | Both | Delivery policy is persistent project configuration, defaulted conservatively; per-task surface classification is intake work, not filename inference; yolo/routine authority is orthogonal to delivery. |
| `skills/project-management` add/create/initialize | Both | Remote creation needs explicit operator consent for exact name/owner/visibility; local creation does not imply a remote; required initialization/auth/daemon failures block dispatch and do not authorize daemon restart. |
| `skills/project-management` removal | Both | Removal requires explicit operator approval plus preflight for tasks, worktrees, dirty files, commits, and other unlanded work; never bypass a failed preflight or force deletion. |
| `skills/stuck-crewmate-recovery` applicability and reconciliation | Both | Use recovery only for the recorded direct worker; endpoint death is not proof work vanished; check authoritative validation/current state and recorded lease/worktree before relaunch; never allocate a duplicate task worktree. |
| `skills/stuck-crewmate-recovery` relaunch/escalation | Both | Preserve identity and unlanded work, intervene from least to most disruptive, do not treat low context as a wedge, and after the second failed relaunch record plain failure with preserved-work consequences. |
| `skills/bootstrap-diagnostics` governing rule | Both | Handle actionable diagnostics before dependent dispatch; detect, obtain current operator consent, then install; use operator-facing consequence language and never silently install or route around missing prerequisites. |
| `skills/bootstrap-diagnostics` MISSING, manual, backend, auth, config | Both | Required dependency/auth/config failures block the dependent action; manual auth/install remains operator work; malformed profiles and invalid adapters are corrected rather than bypassed. |
| `skills/bootstrap-diagnostics` sync, migration, secondmate, FMX cases | Both | Preserve private/state evidence, do not force unsafe recovery, resume only through the owner path after independent verification, and treat a bounded nonblocking sync skip differently from an unsafe stuck state. Secondmate-specific mechanisms are deferred in V1, but the safety rule remains. |
| `skills/harness-adapters` detection, guards, session start, watcher | Both | Detect the active/selected agent through verified evidence; lifecycle guard failures are loud; a tool/harness transition must not leave work unsupervised. Exact hook and watcher recipes are replaced by V1 runtime adapters. |
| `skills/harness-adapters` launch profile and model discovery | Both | Resolve explicit task profile deterministically; validate supported model/effort against the owning adapter/catalog; record uncertainty rather than guessing; reject unsupported combinations. |
| `skills/harness-adapters` no-mistakes invocation/submission hazards | Both | Invoke validation through the worker's supported interface, verify delivery/submission postconditions, and keep validation ownership with the worker/task attempt. |
| `skills/harness-adapters` per-harness sections (Claude, Codex, OpenCode, Pi, Grok, Kimi) | Both | Preserve every stated lifecycle fact as adapter-specific verification evidence: executable discovery, safe launch shape, model/effort support, busy/idle evidence, interrupt/exit behavior, trust handling, prompt delivery, and hook ownership. V1 implements only supported adapters and fails closed for the rest. |

## Implementation priority

The “Both” and “deterministic” portions become Go control-plane invariants in later milestones: task/attempt fencing, legal transitions, lease and worktree ownership, task-to-worker communication boundaries, signal creation/resolution, validation ownership, delivery approval, and teardown protection.
The behavioral portions remain in derived captain and worker prompts: task decomposition, diagnostic judgment, authority interpretation, escalation composition, evidence summaries, and concise status presentation.

No vendored prompt is edited to apply this classification.
Derived prompts must cite this document and the unchanged upstream source they adapt.
