# Agent Contract

This repository uses the immutable GPT Review Planner workflow.

- Workflow repository: `https://github.com/rceman/gpt-review-planner`
- Workflow version: `v1.2.0`
- Workflow commit: `07ab94b358e8634fa0e547acaa0cf6e237ebbc2e`
- Normative document: `GPT_REVIEW_PLANNER.md`

GPT owns architecture, behavior, tests, fixtures, and the principal implementation.
The local agent applies supplied patch packs, runs runtime gates, fixes only verified narrow integration defects, and records evidence.

Do not redesign supplied behavior, broaden scope, add dependencies, weaken tests, or perform an unconstrained repository review during patch-pack execution.

Every executable patch pack must contain `AGENT_HANDOFF.md`. The gateway-generated local `AGENT_HANDOFF.md` is the only normative runtime entry point for the local coding agent; the response file is `AGENT_RESPONSE.md`.
