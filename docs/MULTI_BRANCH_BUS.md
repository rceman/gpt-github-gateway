# Multi-Branch Git Bus

## Branch roles

```text
main
    immutable bootstrap template

gateway/<gateway_id>
    rolling control snapshot; tree contains only gateway.json

project/<gateway_id>/<project_id>
    durable append-only project coordination history
```

`main` is never used for runtime state. Its root commit contains `README.md`, `.gitattributes`, `.gitignore`, and `bootstrap.json`. The gateway pins the exact template commit locally and refuses to run if it changes unexpectedly.

## Control branch

Each gateway is the sole writer of its control branch. `gateway.json` includes gateway version, liveness lease, capabilities, execution mode, runtime status, and the configured project list. It excludes absolute paths, resume IDs, launch arguments, credentials, and private logs.

The gateway publishes immediately at startup, on status changes, every configured heartbeat interval, and at graceful shutdown. Every snapshot commit has `main` as its only parent. The branch ref is replaced with an exact `force-with-lease`, so heartbeat history does not grow.

## Project branches

Each `(gateway_id, project_id)` has one branch:

```text
README.md
PROJECT_CONTEXT.md
project.json
state/checkpoint.json
inbox/.gitkeep
results/.gitkeep
archive/.gitkeep
```

Task paths are `inbox/<task_id>.taskbundle.json`. Final results are `results/<task_id>.result.json`. A result and the updated checkpoint are committed together. Project branches are never force-pushed.

A branch encodes routing, but the gateway still validates the configured identity, branch name, `project.json`, task bundle envelope, and patch manifest target before execution.

## Concurrency

Each project has one serial worker and one Airelay session. Separate projects may execute concurrently. Queued tasks are ordered by `submitted_at` and then `task_id`. Git operations against the shared mirror are serialized across threads and processes.

## Local layout

```text
~/.gpt-github-gateway/<gateway_id>/bus/
├── mirror.git/
├── template.commit
└── projects/
    └── <project_id>/
```

## Migration

Run a dry-run first:

```bash
python3 scripts/migrate-bus-multibranch.py \
  --config "$HOME/.config/gpt-github-gateway/config.json" \
  --dry-run
```

Execution requires explicit repository confirmation:

```bash
python3 scripts/migrate-bus-multibranch.py \
  --config "$HOME/.config/gpt-github-gateway/config.json" \
  --execute \
  --confirm-repository rceman/typer
```

The migration refuses active tasks, creates and verifies a complete Git bundle backup, replaces `main` with a root bootstrap commit, converts configuration atomically, installs the validated gateway, verifies all new branches, and only then deletes legacy refs.
