# Architecture

## Responsibility boundary

`gpt-review-planner` owns executable patch-pack semantics, evidence, and canonical validation. `gpt-github-gateway` owns transport, branch management, safe materialization, local repository isolation, Airelay dispatch, result publication, and recovery. `rceman/typer` is passive private transport.

## Identity hierarchy

```text
gateway_id
└── project_id
    └── task_id
```

The same project may be registered on multiple gateways. Its bus branch is therefore derived from both gateway and project identity.

## Git bus branches

```text
main
    immutable bootstrap root

gateway/<gateway_id>
    rolling latest gateway.json snapshot

project/<gateway_id>/<project_id>
    durable append-only coordination history
```

`main` contains no runtime data. Every control snapshot has `main` as its only parent and is replaced with exact `force-with-lease`. Project branches are never force-pushed.

## Local state

```text
~/.gpt-github-gateway/<gateway_id>/
├── bus/
│   ├── mirror.git/
│   ├── template.commit
│   └── projects/<project_id>/
├── <project_id>/
│   ├── tasks/<task_id>/
│   └── worktrees/<task_id>/
├── daemon.pid
├── daemon.lock
└── gateway.log
```

One bare mirror is shared by project worktrees. Git operations are serialized by an inter-process lock.

## Execution model

Each project owns one worker and one Airelay session. Workers for different projects may run concurrently. A project worker processes tasks serially in `submitted_at`, then `task_id`, order.

1. Fetch and verify immutable `main`.
2. Sync one project branch.
3. Validate task bundle routing and archive safety.
4. Materialize the task locally.
5. Validate patch-pack and source repository identity.
6. Create the isolated source worktree.
7. Dispatch the canonical `AGENT_HANDOFF.md` to the configured Airelay session.
8. Verify implementation, gates, and evidence.
9. Commit the final result and updated checkpoint together to the project branch.
10. Publish active/idle state through the rolling control snapshot.
