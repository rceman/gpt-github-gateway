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

The patch manifest must pin:

```text
repository = https://github.com/rceman/gpt-review-planner
version    = v1.2.0
commit     = 07ab94b358e8634fa0e547acaa0cf6e237ebbc2e
document   = GPT_REVIEW_PLANNER.md
```

The target repository must match the local project registry. The target base revision must exist in the local repository. Manifest file sets are immutable execution scope.

## Agent result

The agent writes `agent-result.json` and `agent-response.md` to the paths named in the generated local request.

A successful result must include every manifest gate exactly once with `status: pass`, plus implementation and evidence commit SHAs.

The Markdown response is intended for GPT review and must remain concise: summary, commands, repairs, deviations, commits, and final state.
