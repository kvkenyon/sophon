# FirstMate role migration

This guide translates responsibilities, authority, and communication paths from the vendored FirstMate baseline to Sophon.
It is deliberately semantic rather than a word substitution guide.

| FirstMate term | Sophon term | Semantic migration |
|---|---|---|
| Captain | Operator | The human remains the final authority, but is addressed and modeled as the operator rather than a nautical role. |
| FirstMate | Commander | The coordinating agent becomes the commander. It plans, delegates, supervises, synthesizes evidence, and routes escalations. |
| Crewmate | Worker | A task-scoped implementation, investigation, or review agent becomes a worker. It owns task execution, not operator interaction or platform authority. |
| Fleet | Mission and workers | A fleet is split into the durable mission plus its tasks, attempts, and assigned workers. Do not treat it as an informal collection of terminal sessions. |
| Captain decision | Operator signal | An unresolved human decision becomes a durable signal owned by the mission/control plane, with its evidence, dependencies, and resolution recorded. |
| SecondMate | Removed in V1 | V1 has no nested coordinator/home topology. The commander delegates directly to workers; do not emulate secondmate routing, provisioning, or independent backlogs. |

## Authority and communication

The operator is the only human authority for product decisions, destructive or irreversible actions, security-sensitive choices, and merges unless an explicit V1 policy grants a bounded routine decision.
The commander is the operator's only agent-facing coordinator.
Workers communicate findings, completion evidence, and blockers to the commander through structured task state.
Workers do not contact the operator directly.

FirstMate's instruction that its firstmate never edits a project becomes a separation of duties: the Sophon commander plans and coordinates while the assigned worker performs project changes in its leased worktree.
The control plane, rather than conversational convention, owns task state, attempts, leases, transitions, signals, and delivery records.

## Task shapes

FirstMate's `ship` maps to an implementation task that follows the task's selected delivery path.
FirstMate's `scout` maps to an investigation or review task whose primary artifact is a report; its recommendation is evidence, not authorization for a later implementation task.
An explicit promotion from a scout to implementation is a new controlled transition with preserved evidence, not an informal continuation.

## Language migration

Retain the baseline's behavioral intent while translating audience language:

- Say “operator” for the human and “commander” for the coordinating agent.
- Say “worker” for a delegated agent and “mission” or “mission workers” for the coordinated set.
- Present durable unresolved choices as operator signals, not “operator holds.”
- Remove nautical salutation and seasoning requirements from product-facing prompts; they are FirstMate presentation policy, not Sophon identity.
- Replace FirstMate filesystem, watcher, terminal, and home-local terminology with the corresponding control-plane concepts only where the behavior is retained.

Do not mechanically replace words inside vendored upstream files.
Those files are preserved as provenance; later derived prompts apply this migration at their behavioral boundaries.
