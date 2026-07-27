# Architecture

## Responsibility boundary

`gpt-review-planner` is the normative development methodology. `gpt-github-gateway` is an enforcement and transport component.

GPT owns architecture, behavior contracts, fixtures, tests, production code, static review, patch packs, and post-execution delta review. The local execution side owns repository identity checks, patch application, dependency restoration, runtime gates, narrow integration corrections, commits, and evidence.

The gateway deliberately prevents the execution side from falling back to the old workflow where a local model explores the repository and invents an implementation from prose.

## Identity hierarchy

```text
gateway_id
└── project_id
    └── task_id
```

A `project_id` is unique only inside its gateway. The same repository may be configured on several machines:

```text
home_pc / ai-workspace
work_pc / ai-workspace
```

Every remote task contains both IDs. A gateway processes only its own subtree.

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
            ├── request.md
            ├── patch-pack/
            ├── agent-request.md
            ├── agent-response.md
            ├── agent-result.json
            ├── approval.json
            └── status.json
```

The configured source checkout is used only as the owning Git repository for `git worktree`. It is not switched, stashed, reset, or cleaned.

## Airelay session model

Each local project has an Airelay profile and stable session key. The default key is `<project_id>_master`.

When the session is not active and a local `resume_session_id` is configured, the gateway starts it using the local configuration equivalent of:

```bash
airelay start codex --key <session_key> -- \
  resume <resume_session_id> \
  --dangerously-bypass-approvals-and-sandbox
```

The remote task cannot alter any part of this launch contract. After patch application the gateway sends a short prompt:

```bash
airelay prompt <session_key> --text \
  "Read <absolute-agent-request-path> and execute it." \
  --no-sender
```

Detailed instructions remain in files, not in prompt arguments.

## Execution phases

1. Sync the Git bus with fast-forward-only semantics.
2. Discover tasks addressed to the configured gateway.
3. Copy the immutable task payload into local state.
4. Validate gateway/project/task identity and the pinned workflow.
5. Wait for local owner approval when required.
6. Verify repository identity and target base revision.
7. Create a task branch and isolated worktree.
8. Create `refs/gpt-gateway/backups/<task_id>` at the base revision.
9. Apply the patch with `git apply --check` and `git apply --3way`.
10. Verify declared file-operation scope before agent execution.
11. Generate a bounded agent request.
12. Prompt the configured Airelay session.
13. Wait for `agent-result.json` and `agent-response.md`.
14. Verify result identity, required gate results, commits, and evidence.
15. Publish machine status and the concise response to the bus.
16. GPT performs a bounded delta review.

A failed patch may be handed to the agent for a narrow manual application only when the local project policy allows it. The request still locks the manifest scope and behavior contract.
