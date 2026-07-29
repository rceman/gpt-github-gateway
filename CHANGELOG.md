# Changelog

## 0.4.0 — 2026-07-29

- Replace legacy dual `agent-result.json`/`AGENT_RESPONSE.md` completion with one strict JSON result.
- Generate a task-local `complete-task` command that validates the authoritative result before daemon publication.
- Resume interrupted `agent_running` tasks after gateway restart instead of permanently blocking the project queue.
- Detect a promptable Airelay session without finalization, issue one corrective reprompt, then publish a bounded synthetic failure.
- Pin executable task semantics to gpt-review-planner 1.3.0 commit `f8cb8bc67c138f7e0e026c9270d3bd89dcd855d1`.

## 0.3.0 - 2026-07-29

- Replace the single shared bus branch with an immutable `main` template, one rolling control branch per gateway, and one append-only branch per gateway project.
- Add schema-v2 configuration, branch-pattern validation, a shared bare mirror, isolated project worktrees, and cross-process Git operation locking.
- Add one-file `gateway.json` control snapshots with heartbeat leases and force-with-lease replacement.
- Add deterministic project branch bootstrap, per-project queues, atomic result/checkpoint commits, and bounded non-fast-forward retries.
- Add the guarded `scripts/migrate-bus-multibranch.py` migration and rollback bundle workflow.

## 0.2.0 - 2026-07-28

- Add atomic protocol-v2 task bundles for one-file Git bus submission.
- Default trusted local gateways to automatic Airelay dispatch with optional manual or disabled modes.
- Add deterministic JSON task-bundle builder and transport validator utilities.
- Harden archive extraction against traversal, links, special files, duplicates, and case-fold collisions.
- Permit normal angle brackets and shell redirection in `AGENT_HANDOFF.md` while rejecting explicit placeholders.
- Resume stale approval-waiting tasks automatically when the local gateway runs in auto mode.

## 0.1.1 - 2026-07-28

- Require canonical `AGENT_HANDOFF.md` for every executable patch pack.
- Use `AGENT_RESPONSE.md` as the local agent response contract.
- Add a one-command local install, configuration, start, and readiness bootstrap.

## 0.1.0 - 2026-07-27

- Add the standalone Go gateway daemon and CLI.
- Add multi-gateway and per-gateway project addressing.
- Add strict GPT Review Planner v1.2.0 patch-pack enforcement.
- Add isolated Git worktree application and rollback.
- Add Airelay-backed long-lived Codex session transport.
- Add local task files, owner approval, Git bus reporting, and loopback API.
