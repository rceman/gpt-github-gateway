# Security

## Trust boundary

The private Git bus is transport, not authority. Local configuration controls repository paths, target identities, execution mode, Airelay binary, profile, session key, resume ID, and launch arguments. Remote tasks cannot alter those values.

## Branch isolation

- `main` is immutable and locally pinned.
- each control branch has one writer: its gateway;
- each project branch is isolated by gateway and project identity;
- control updates require exact `force-with-lease`;
- project branches reject force pushes and retry normal non-fast-forward conflicts;
- a task placed in the wrong project branch is rejected before local worktree creation.

## Archive safety

Protocol-v2 archives accept regular files only and reject traversal, absolute or backslash paths, links, devices, duplicate paths, case-fold collisions, non-deterministic metadata, invalid digest or sizes, and changed content under an existing task ID.

## Local Git safety

The gateway uses a bare bus mirror and dedicated project worktrees. It never switches, resets, stashes, cleans, or commits in the owner's source checkout. Shared mirror operations are protected by an inter-process lock.

## Migration safety

The migration defaults to dry-run, requires explicit repository confirmation, refuses active tasks, creates and verifies a complete Git bundle backup, uses `force-with-lease` for `main`, validates all new branches, and deletes legacy refs only after readiness and branch verification pass.

## Published metadata

`gateway.json` excludes absolute local paths, resume UUIDs, launch arguments, environment variables, credentials, and private logs.
