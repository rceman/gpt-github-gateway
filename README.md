# gpt-github-gateway

GitHub-backed local execution gateway for GPT-authored `gpt-review-planner` patch packs.

## Runtime model

```text
GPT
  → project/<gateway_id>/<project_id>/inbox/<task_id>.taskbundle.json
  → gpt-github-gateway validates and materializes the bundle
  → isolated source-repository worktree
  → Airelay long-lived project session
  → implementation and evidence commits
  → project branch results/<task_id>.result.json + checkpoint
  → GPT delta review
```

## Bus branches

```text
rceman/typer:main
    immutable bootstrap template

rceman/typer:gateway/<gateway_id>
    rolling one-file gateway.json snapshot

rceman/typer:project/<gateway_id>/<project_id>
    durable project-specific task and result history
```

For `home_pc` the initial project branches are:

```text
project/home_pc/gpt-github-gateway
project/home_pc/gpt-review-planner
project/home_pc/airelay
```

The control branch contains only `gateway.json`. Project branches contain `project.json`, `PROJECT_CONTEXT.md`, `state/checkpoint.json`, `inbox/`, `results/`, and `archive/`.

## Core properties

- immutable locally pinned `main` template;
- independent control branch per gateway machine;
- independent append-only branch per gateway project;
- one serial worker and one Airelay session per project;
- separate projects may execute concurrently;
- one-file protocol-v2 task submission and one strict agent-authored JSON result and one final result commit;
- strict identity validation across config, branch, `project.json`, task envelope, and patch manifest;
- no remote task may select local paths, binaries, session IDs, execution mode, or launch arguments;
- shared bare mirror with isolated project worktrees and cross-process Git operation locking;
- control heartbeat snapshots use exact `force-with-lease`; project branches never force-push.

## Install

```bash
bash scripts/install.sh
```

## New configuration

```bash
gpt-github-gateway init \
  --gateway home_pc \
  --bus-repository rceman/typer \
  --bus-url git@github.com:rceman/typer.git
```

Add a project:

```bash
gpt-github-gateway project add \
  --id gpt-github-gateway \
  --path "$HOME/.gpt-github-gateway/sources/gpt-github-gateway" \
  --repository rceman/gpt-github-gateway \
  --branch main \
  --session-key gpt-github-gateway_master \
  --resume-session <CODEX_SESSION_ID>
```

## Upgrade from the single-branch bus

Dry-run:

```bash
python3 scripts/migrate-bus-multibranch.py \
  --config "$HOME/.config/gpt-github-gateway/config.json" \
  --dry-run
```

Execute only after validating gateway `0.3.0`:

```bash
python3 scripts/migrate-bus-multibranch.py \
  --config "$HOME/.config/gpt-github-gateway/config.json" \
  --execute \
  --confirm-repository rceman/typer
```

The migration creates a verified rollback bundle before changing `rceman/typer`.

## Commands

```text
init
project add
projects
once
run
start
stop
status
doctor
tasks
task approve <project_id> <task_id>
task reject <project_id> <task_id> [reason]
task rollback <project_id> <task_id>
version
```

The loopback API listens on `127.0.0.1:8787` by default.

See `docs/ARCHITECTURE.md`, `docs/PROTOCOL.md`, `docs/MULTI_BRANCH_BUS.md`, and `docs/SECURITY.md`.

## JSON-only task completion

Gateway 0.4.0 appends an authoritative `agent-result.json` path and generated `complete-task` command to every runtime handoff. The agent writes the JSON and invokes that command for `succeeded`, `needs_gpt_revision`, or `failed`. Interactive Airelay text and `AGENT_RESPONSE.md` are not terminal artifacts. If the session becomes promptable without finalization, the gateway sends one corrective prompt and then creates a synthetic failure so the queue cannot remain blocked until the global timeout.
