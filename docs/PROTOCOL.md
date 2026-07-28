# Git Bus Protocol v1

## Repository layout

```text
protocol/v1/
gateways/<gateway_id>/gateway.json
gateways/<gateway_id>/projects/<project_id>/state.json
tasks/<gateway_id>/<project_id>/<task_id>/
├── task.json
├── request.md
├── patch-pack/
│   ├── AGENT_HANDOFF.md
│   ├── manifest.json
│   ├── evidence.json
│   ├── patch/changes.patch
│   ├── patch/delete-paths.txt
│   ├── overlay/
│   └── scripts/
└── agent/
    ├── status.json
    ├── response.md
    └── result.json
```

GPT owns `task.json`, `request.md`, and `patch-pack/`. The gateway owns `agent/`. Neither side mutates files owned by the other side.

## Task envelope

Only `apply_patch_pack` is executable in protocol v1. The task envelope contains identity and references. It does not contain commands.

```json
{
  "schema_version": 1,
  "task_id": "task_001",
  "gateway_id": "home_pc",
  "project_id": "gpt-github-gateway",
  "operation": "apply_patch_pack",
  "submitted_at": "2026-07-27T18:00:00Z",
  "request_path": "request.md",
  "patch_pack_path": "patch-pack",
  "result_branch": "agent/task_001-gateway-foundation",
  "approval_required": true
}
```

Paths are normalized relative paths inside the task directory. Absolute paths, backslashes, empty segments, `.` and `..` are rejected.

## Workflow enforcement

Every executable patch pack must contain `patch-pack/AGENT_HANDOFF.md`. It is the single canonical instruction entry point for the local agent and must identify the patch, repository, base revision, and immutable workflow pin. The gateway rejects missing, incomplete, symlinked, oversized, or placeholder-containing handoffs. It copies the validated handoff to the local task root, appends machine-local runtime paths, and sends only a short Airelay prompt pointing to that file.

The patch manifest must pin:

```text
repository = https://github.com/rceman/gpt-review-planner
version    = v1.2.0
commit     = 07ab94b358e8634fa0e547acaa0cf6e237ebbc2e
document   = GPT_REVIEW_PLANNER.md
```

The target repository must match the local project registry. The target base revision must exist in the local repository. Manifest file sets are immutable execution scope.

## Agent result

The agent writes `agent-result.json` and `AGENT_RESPONSE.md` to the paths named in the gateway runtime appendix of `AGENT_HANDOFF.md`.

A successful result must include every manifest gate exactly once with `status: pass`, plus implementation and evidence commit SHAs.

The Markdown response is intended for GPT review and must remain concise: summary, commands, repairs, deviations, commits, and final state.


## Protocol v2 atomic submission

New tasks use one file:

```text
inbox/<gateway_id>/<project_id>/<task_id>.taskbundle.json
```

The JSON contains routing metadata, a strictly validated structured task document, and one deterministic tar.gz patch pack encoded as base64 with exact SHA-256 and size metadata. The task document is materialized as `TASK_REQUEST.json`; Markdown is reserved for the canonical agent execution handoff. The gateway validates and materializes the entire bundle atomically before creating a worktree or contacting Airelay.

The local gateway configuration controls execution with `task_execution_mode = auto | manual | disabled`. Remote task data cannot select the Airelay binary, session key, resume session, launch arguments, repository path, or local execution policy.

Protocol-v2 results use one file:

```text
results/<gateway_id>/<project_id>/<task_id>.result.json
```

See `TASK_BUNDLE_V2.md` and `protocol/v2/*.schema.json`. Protocol v1 remains read-compatible for existing audit records.
