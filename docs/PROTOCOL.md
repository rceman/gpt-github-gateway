# Protocol

## Bootstrap template

`main:bootstrap.json` declares template version 1, branch patterns, the one-file control tree, and project layout version. The gateway pins the exact template commit locally and rejects unexpected changes.

## Control branch

```text
gateway/<gateway_id>
└── gateway.json
```

`gateway.json` contains public coordination metadata only: gateway ID and version, liveness lease, capabilities, execution mode, runtime readiness, and projects. It never contains local paths, resume IDs, launch arguments, credentials, or private logs.

The control branch is a rolling snapshot. Every snapshot commit is created directly on top of `main` and pushed with exact `force-with-lease`.

## Project branch

```text
project/<gateway_id>/<project_id>/
├── README.md
├── PROJECT_CONTEXT.md
├── project.json
├── state/checkpoint.json
├── inbox/
├── results/
└── archive/
```

Task submission:

```text
inbox/<task_id>.taskbundle.json
```

Final publication:

```text
results/<task_id>.result.json
state/checkpoint.json
```

The result and checkpoint are one normal append-only commit.

## Identity validation

Before dispatch, the following must agree:

- local configured `gateway_id` and `project_id`;
- expanded project branch name;
- `project.json`;
- task bundle envelope;
- patch manifest target repository and branch.

## Queueing

Only one task may execute per project. Several project workers may execute concurrently. A task already represented by a final result file is not redispatched.

## Compatibility

Protocol-v2 task bundle and result payloads remain unchanged. The branch-local paths replace the former nested shared-branch paths. Protocol-v1 records may remain in backups but are not accepted as new multi-branch submissions.


## JSON-only terminal protocol (gateway 0.4.0)

The only agent-authored terminal artifact is `<task-root>/agent-result.json` using schema version 2. The runtime handoff includes `<task-root>/complete-task`; the agent must invoke it for every terminal status. The command validates task identity, schema semantics, manifest gate coverage, successful commit ancestry, exact result-branch remote head, worktree cleanliness, declared implementation scope, and committed evidence before writing an atomic completion marker.

The daemon waits for that marker rather than `AGENT_RESPONSE.md`. When the Airelay session becomes promptable without a marker, the daemon issues one bounded corrective prompt. A second promptable observation after the grace period produces a synthetic `failed` result and completion marker, allowing atomic bus publication and queue advancement. Restarted gateways resume `agent_running` tasks instead of treating them as permanently blocking local state.
