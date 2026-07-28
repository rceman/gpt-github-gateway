# Architecture

## Responsibility boundary

`gpt-review-planner` owns the normative executable patch-pack format and semantic validation. `gpt-github-gateway` owns transport validation, safe local materialization, repository isolation, Airelay dispatch, result publication, and recovery. `rceman/typer` remains a passive private Git bus.

GPT owns architecture, behavior contracts, fixtures, tests, production code, static review, patch packs, and post-execution delta review. The local execution side owns repository identity checks, patch application, dependency restoration, runtime gates, narrow integration corrections, commits, and evidence.

## Identity hierarchy

```text
gateway_id
└── project_id
    └── task_id
```

A `project_id` is unique only inside its gateway. Every remote task contains all three IDs. A gateway processes only its own inbox subtree.

## Git bus

Protocol v2 uses one immutable input file and one final output file:

```text
inbox/<gateway_id>/<project_id>/<task_id>.taskbundle.json
results/<gateway_id>/<project_id>/<task_id>.result.json
```

The input contains a structured JSON task document plus a deterministic base64-encoded tar.gz patch pack. Publishing the one complete file atomically submits the task. Intermediate execution state is local and is not committed to the bus.

## Local state

```text
~/.gpt-github-gateway/<gateway_id>/
├── bus/
├── daemon.pid
├── gateway.log
└── <project_id>/
    ├── worktrees/
    │   └── <task_id>/
    └── tasks/
        └── <task_id>/
            ├── task.json
            ├── TASK_REQUEST.json
            ├── patch-pack/
            ├── AGENT_HANDOFF.md
            ├── AGENT_RESPONSE.md
            ├── agent-result.json
            ├── status.json
            └── .taskbundle-sha256
```

The configured source checkout is used only as the owning Git repository for `git worktree`. It is not switched, stashed, reset, cleaned, or committed by gateway execution.

## Airelay session model

Each local project has an Airelay profile and stable session key. The default key is `<project_id>_master`.

When the session is not active and a local `resume_session_id` is configured, the gateway starts it from local configuration. Remote task data cannot alter the executable, profile, session key, resume ID, launch arguments, repository path, or sandbox policy.

After task validation and patch application, the gateway sends only:

```text
Read <absolute-local-AGENT_HANDOFF.md-path> and execute it exactly.
```

Detailed instructions remain in the validated handoff file.

## Local execution modes

The local configuration selects one mode:

- `auto`: dispatch every valid private-bus task immediately;
- `manual`: wait for explicit local approval;
- `disabled`: materialize but refuse execution.

The default is `auto`. A remote task cannot select or weaken this policy.

## Execution phases

1. Fast-forward the Git bus.
2. Discover protocol-v2 bundles addressed to the gateway; retain protocol-v1 read compatibility.
3. Validate bundle JSON, routing identity, base64, digest, sizes, archive metadata, and safe paths.
4. Atomically materialize the structured task document and patch pack.
5. Validate manifest, handoff identity, project identity, and target base revision.
6. Apply the local execution policy.
7. Create a task branch, backup ref, and isolated worktree.
8. Apply the supplied patch and verify exact declared file-operation scope.
9. Append machine-local runtime paths to `AGENT_HANDOFF.md`.
10. Ensure the configured Airelay session is reachable and dispatch the short prompt.
11. Wait for `agent-result.json` and `AGENT_RESPONSE.md`.
12. Verify result identity, gates, commits, and committed planner evidence.
13. Publish one final protocol-v2 result JSON file.
14. GPT performs a bounded delta review.

A failed patch may be handed to the agent for narrow manual integration only when the local project policy permits it. The manifest scope and behavior contract remain locked.
