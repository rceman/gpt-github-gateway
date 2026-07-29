# Atomic Task Bundle Protocol v2

## Purpose

Protocol v2 submits one complete executable task as one UTF-8 JSON file and one Git commit. The Git bus remains a passive transport; patch-pack semantics remain owned by the exact `gpt-review-planner` revision pinned by `manifest.json`.

## Canonical paths

```text
project/<gateway_id>/<project_id>:inbox/<task_id>.taskbundle.json
project/<gateway_id>/<project_id>:results/<task_id>.result.json
```

The bundle contains a deterministic tar.gz patch pack encoded as base64. The task is visible to the gateway only after the single JSON file exists, so partial multi-commit uploads cannot execute.

## Authoring format

`task-spec.json` is the canonical structured authoring document. It contains validated fields for title, summary, objectives, constraints, acceptance criteria, and optional references; protocol v2 materializes it locally as `TASK_REQUEST.json`. Run:

```bash
python3 scripts/build-task-bundle.py \
  --planner-root /absolute/path/to/gpt-review-planner \
  --pack-root /absolute/path/to/patch-pack \
  --task-spec /absolute/path/to/task-spec.json \
  --output /tmp/<task_id>.taskbundle.json
```

The builder resolves the exact planner commit from `manifest.workflow.commit`, runs its semantic patch-pack validator, creates a deterministic archive, validates the completed bundle, and atomically writes one output file.

JSON is canonical rather than YAML because the gateway uses the Go standard library, JSON has unambiguous duplicate-key and scalar semantics, and canonical JSON can be validated identically by GPT-side Python and gateway-side Go. YAML may be used by a future UI as an authoring convenience only after conversion to canonical JSON; it is never accepted directly by the gateway.

## Local execution policy

`gateway.task_execution_mode` controls dispatch:

- `auto`: validated tasks immediately dispatch to the configured Airelay session;
- `manual`: every task waits for a local approval file;
- `disabled`: tasks are materialized but execution is refused.

The default is `auto`. A remote task cannot weaken or override the local policy.

## Archive security

The transport accepts regular-file-only archives and rejects invalid base64, checksum or size mismatches, absolute paths, parent traversal, backslash paths, symlinks, hardlinks, devices, special files, duplicate paths, case-folding collisions, non-deterministic metadata, oversized entries, and missing `AGENT_HANDOFF.md` or `manifest.json`.

Task identity is immutable. A local task directory created from one archive SHA-256 cannot be replaced by different content using the same task ID.

## Compatibility

Protocol-v1 directory tasks remain readable. New GPT-authored work should use protocol v2. Result publication for v2 uses one JSON file containing the final state, commits, gates, deviations, and human response.
