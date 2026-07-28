# gpt-github-gateway

GitHub-backed local gateway for GPT-authored patch execution, validation, and agent-assisted repair.

`gpt-github-gateway` is the transport and enforcement layer for projects developed with [`gpt-review-planner`](https://github.com/rceman/gpt-review-planner). GPT remains the principal architect, reviewer, test author, and implementation author. The gateway addresses work to a specific machine and project, safely applies the supplied patch pack in an isolated Git worktree, and sends a short file-based instruction to a long-lived Codex session through `airelay`.

## Runtime model

```text
GPT
  │ builds one validated taskbundle.json
  ▼
rceman/typer
  │ inbox/<gateway_id>/<project_id>/<task_id>.taskbundle.json
  ▼
gpt-github-gateway
  │ safe extraction + isolated Git worktree + automatic dispatch
  ▼
airelay prompt <project_session_key> "Read <AGENT_HANDOFF.md>"
  │
  ▼
Codex Luna Low
  │ runtime gates + narrow integration fixes + evidence
  ▼
rceman/typer task response
  ▼
GPT delta review
```

## Core properties

- multiple gateway instances such as `home_pc` and `work_pc`;
- projects are local to one gateway and are addressed by `gateway_id + project_id`;
- project `session_key` defaults to `<project_id>_master`;
- no remote task may choose an executable, shell command, Airelay profile, session key, resume ID, or sandbox policy;
- every executable patch pack must contain a complete canonical `AGENT_HANDOFF.md`;
- the gateway appends local runtime paths to that handoff and expects `AGENT_RESPONSE.md` plus `agent-result.json`;
- task instructions and agent responses are files under `~/.gpt-github-gateway/<gateway_id>/<project_id>/tasks/<task_id>/`;
- the user's existing project checkout is never switched, stashed, reset, or cleaned;
- patch application occurs in a task-specific Git worktree and can be rolled back independently;
- the pinned `gpt-review-planner` manifest, declared file scope, base revision, and evidence contract are mandatory;
- protocol-v2 tasks are one deterministic base64 tar.gz bundle inside one JSON file and one Git commit;
- local `task_execution_mode` defaults to `auto`; `manual` and `disabled` remain emergency policies;
- JSON is the canonical task authoring and transport format; Markdown remains the final agent-readable `AGENT_HANDOFF.md`.

## Install

```bash
bash scripts/install.sh
```

Or build and install directly:

```bash
go install github.com/rceman/gpt-github-gateway/cmd/gpt-github-gateway@latest
```

## Bootstrap

```bash
gpt-github-gateway init \
  --gateway home_pc \
  --bus-repository rceman/typer \
  --bus-url git@github.com:rceman/typer.git \
  --bus-branch ai-workspace-bus

gpt-github-gateway project add \
  --id gpt-github-gateway \
  --path "$HOME/git/gpt-github-gateway" \
  --repository rceman/gpt-github-gateway \
  --resume-session 019efe9b-294a-7362-84da-875a68bbd645
```

The generated session key is `gpt-github-gateway_master`. It can be overridden only in the local configuration.

For the first machine bootstrap, install, configure, start, and verify the daemon in one command:

```bash
bash scripts/bootstrap-local.sh \
  --gateway home_pc \
  --project-path "$PWD" \
  --resume-session <CODEX_SESSION_ID>
```

The script never stores GitHub or Airelay credentials and uses existing host Git/SSH and Airelay configuration.

## Commands

```text
gpt-github-gateway init
gpt-github-gateway project add
gpt-github-gateway projects
gpt-github-gateway once
gpt-github-gateway run
gpt-github-gateway start
gpt-github-gateway stop
gpt-github-gateway status
gpt-github-gateway doctor
gpt-github-gateway tasks
gpt-github-gateway task approve <project_id> <task_id>
gpt-github-gateway task reject <project_id> <task_id> [reason]
gpt-github-gateway task rollback <project_id> <task_id>
```

The local HTTP API listens on `127.0.0.1:8787` by default and exposes health, readiness, status, project, and task views for a future AI Workspace UI.

See `docs/ARCHITECTURE.md`, `docs/PROTOCOL.md`, `docs/TASK_BUNDLE_V2.md`, and `docs/SECURITY.md`.
