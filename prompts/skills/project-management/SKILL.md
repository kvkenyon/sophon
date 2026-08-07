---
name: project-management
description: Agent-only procedure for adding, creating, registering, initializing, or removing a Sophon project with explicit destination, delivery posture, consent, and unlanded-work protection.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/project-management/SKILL.md. -->

# Project management

Use this procedure before adding, creating, registering, initializing, or removing a project.
The control plane owns project identity and registry state.
Do not overwrite, repurpose, or silently duplicate an existing project.

## Preconditions

Inspect authoritative registered projects with `await sophon.projects()` or `sophon project list --json`.
Resolve the source, project name, destination, delivery posture, and operator authority before mutation.
Keep a newly created local artifact and its registry record consistent.
On partial failure, roll back only artifacts created by that operation and only when the rollback is demonstrably safe.

Use one of the V1 delivery modes as the standing project posture:

- **Gate** for configured validation, PR creation, CI, and verified delivery;
- **PR** for verified push and PR creation without the Gate pipeline; or
- **Branch** for a retained delivered branch.

Merge is unavailable in V1.
Task-specific delivery remains an intake decision and must not be inferred from filenames or directory names.
Delivery rigor and routine decision authority are separate.

## Add an existing local project

Confirm the local source and resolved delivery posture, then use `sophon project add .` from the intended project only after proving it is the exact operator-authorized source and not already registered.
Inspect the resulting record with `sophon project inspect NAME --json`.
Do not treat registration as authority to change the project's files or create a remote.

## Clone, create, and initialize

Creating a remote repository is outward-facing.
Obtain explicit operator consent for the exact owner, repository name, visibility, and delivery posture before creating it.
Default suggestions are not consent.
A request for a local project does not authorize an unmentioned remote.

`TODO(spec-gap)`: V1 does not define project clone, remote-create, or project-initialization APIs.
Until those operations have supported control-plane surfaces, do not invent shell procedures; report the missing capability and preserve any existing state.

## Remove

Removal is destructive and requires explicit operator approval for the exact project.
Before removal, inspect structured missions, tasks, attempts, leases, delivery records, and Signals, then inspect the repository for dirty files, local commits, linked worktrees, and other unlanded work through an authorized worker when project access is required.
If any dependency or unlanded work exists, stop and report it.

`TODO(spec-gap)`: V1 does not define a project-removal command.
Do not bypass that gap with direct deletion.
