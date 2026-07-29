#!/usr/bin/env python3
"""Safely repurpose rceman/typer into the gateway multi-branch bus."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

TERMINAL_STATES = {
    "succeeded", "completed", "failed", "needs_gpt_revision", "rejected",
    "rolled_back", "agent_timeout", "agent_unavailable", "execution_disabled",
    "superseded", "cancelled",
}
REQUIRED_PROJECTS = {
    "gpt-github-gateway": ("rceman/gpt-github-gateway", "main", "gpt-github-gateway_master"),
    "gpt-review-planner": ("rceman/gpt-review-planner", "main", "gpt-review-planner_master"),
    "airelay": ("therceman/airelay", "master", "airelay_master"),
}
FINAL_BRANCHES = {
    "main",
    "gateway/home_pc",  # replaced with actual gateway id at runtime
    "project/home_pc/gpt-github-gateway",
    "project/home_pc/gpt-review-planner",
    "project/home_pc/airelay",
}
TARGET_GATEWAY_VERSION = "0.3.0"


def run(args: list[str], *, cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(args, cwd=cwd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and result.returncode != 0:
        raise RuntimeError(f"command failed ({result.returncode}): {' '.join(args)}\n{result.stderr.strip()}")
    return result


def utc_stamp() -> str:
    return datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")


def atomic_json(path: Path, data: dict[str, Any], mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_suffix(path.suffix + ".tmp")
    encoded = (json.dumps(data, indent=2) + "\n").encode()
    fd = os.open(temp, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, mode)
    try:
        os.write(fd, encoded)
        os.fsync(fd)
    finally:
        os.close(fd)
    os.replace(temp, path)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def list_remote_refs(url: str) -> tuple[dict[str, str], dict[str, str]]:
    result = run(["git", "ls-remote", "--heads", "--tags", url])
    branches: dict[str, str] = {}
    tags: dict[str, str] = {}
    for line in result.stdout.splitlines():
        if not line.strip():
            continue
        sha, ref = line.split("\t", 1)
        if ref.startswith("refs/heads/"):
            branches[ref.removeprefix("refs/heads/")] = sha
        elif ref.startswith("refs/tags/") and not ref.endswith("^{}"):
            tags[ref.removeprefix("refs/tags/")] = sha
    return branches, tags


def scan_active_tasks(gateway_root: Path, project_ids: Iterable[str]) -> list[tuple[str, str]]:
    blocking: list[tuple[str, str]] = []
    for project_id in project_ids:
        tasks_root = gateway_root / project_id / "tasks"
        if not tasks_root.is_dir():
            continue
        for task_root in sorted(path for path in tasks_root.iterdir() if path.is_dir()):
            status_path = task_root / "status.json"
            if not status_path.exists():
                continue
            try:
                payload = json.loads(status_path.read_text(encoding="utf-8"))
            except Exception as exc:  # noqa: BLE001
                blocking.append((str(status_path), f"invalid status JSON: {exc}"))
                continue
            state = str(payload.get("state", ""))
            if state not in TERMINAL_STATES:
                blocking.append((str(status_path), state or "missing state"))
    return blocking


def delete_remote_ref(url: str, ref: str) -> None:
    if not (ref.startswith("refs/heads/") or ref.startswith("refs/tags/")):
        raise RuntimeError(f"unsupported remote ref: {ref}")
    run(["git", "push", url, f":{ref}"])


def verify_binary_version(binary: Path) -> str:
    output = run([str(binary), "version"]).stdout.strip()
    if output != TARGET_GATEWAY_VERSION:
        raise RuntimeError(f"installed binary version must be {TARGET_GATEWAY_VERSION}, got {output}")
    return output


def preflight_source(source_root: Path) -> dict[str, str]:
    version_path = source_root / "VERSION"
    if not (source_root / ".git").exists():
        raise RuntimeError(f"source root is not a Git repository: {source_root}")
    version = version_path.read_text(encoding="utf-8").strip()
    if version != TARGET_GATEWAY_VERSION:
        raise RuntimeError(f"source version must be {TARGET_GATEWAY_VERSION}, got {version}")
    commit = run(["git", "-C", str(source_root), "rev-parse", "HEAD"]).stdout.strip()
    with tempfile.TemporaryDirectory(prefix="gpt-gateway-candidate-") as temp_name:
        candidate = Path(temp_name) / "gpt-github-gateway"
        run(["go", "build", "-o", str(candidate), "./cmd/gpt-github-gateway"], cwd=source_root)
        output = run([str(candidate), "version"]).stdout.strip()
        if output != TARGET_GATEWAY_VERSION:
            raise RuntimeError(f"candidate binary version must be {TARGET_GATEWAY_VERSION}, got {output}")
    return {"source_commit": commit, "source_version": version}


def new_main_files() -> dict[str, str]:
    bootstrap = {
        "schema_version": 1,
        "template_version": 1,
        "repository_role": "gpt-github-gateway-bus",
        "default_branch": "main",
        "control_branch_pattern": "gateway/{gateway_id}",
        "project_branch_pattern": "project/{gateway_id}/{project_id}",
        "control_tree": ["gateway.json"],
        "project_layout_version": 1,
    }
    readme = """# GPT GitHub Gateway Bus

Private passive Git transport for `gpt-github-gateway`.

`main` is the immutable bootstrap template. Gateway runtime never writes to it.

- `gateway/<gateway_id>` is a rolling control snapshot containing only `gateway.json`.
- `project/<gateway_id>/<project_id>` is a durable append-only project history containing project context, task bundles, final results, and a checkpoint.
- `gpt-review-planner` owns executable patch-pack semantics.
- `gpt-github-gateway` owns transport, branch management, safe materialization, dispatch, and result publication.
- `rceman/typer` contains no application implementation and acts only as private transport.
"""
    return {
        "README.md": readme,
        ".gitattributes": "* text=auto eol=lf\n*.json text eol=lf\n*.md text eol=lf\n",
        ".gitignore": ".DS_Store\n*.swp\n*.tmp\n*.lock\n",
        "bootstrap.json": json.dumps(bootstrap, indent=2) + "\n",
    }


def convert_config(old: dict[str, Any], airelay_path: Path) -> dict[str, Any]:
    gateway_id = old.get("gateway", {}).get("id")
    if not gateway_id:
        raise RuntimeError("config gateway.id is missing")
    projects = dict(old.get("projects") or {})
    for project_id, (repository, default_branch, session_key) in REQUIRED_PROJECTS.items():
        if project_id not in projects:
            if project_id != "airelay":
                raise RuntimeError(f"required existing project {project_id} is missing from config")
            projects[project_id] = {
                "path": str(airelay_path),
                "repository": repository,
                "default_branch": default_branch,
                "airelay_profile": "codex",
                "session_key": session_key,
                "launch_args": ["--dangerously-bypass-approvals-and-sandbox"],
            }
        else:
            project = projects[project_id]
            if project.get("repository") != repository:
                raise RuntimeError(f"project {project_id} repository mismatch")
            project.setdefault("default_branch", default_branch)
            project.setdefault("airelay_profile", "codex")
            project.setdefault("session_key", session_key)
    return {
        "schema_version": 2,
        "gateway": old["gateway"],
        "bus": {
            "repository": old.get("bus", {}).get("repository", "rceman/typer"),
            "url": old.get("bus", {}).get("url", "git@github.com:rceman/typer.git"),
            "template_branch": "main",
            "control_branch_pattern": "gateway/{gateway_id}",
            "project_branch_pattern": "project/{gateway_id}/{project_id}",
            "heartbeat_interval_seconds": 600,
            "lease_duration_seconds": 1500,
        },
        "server": old.get("server", {"listen": "127.0.0.1:8787"}),
        "airelay": old.get("airelay", {"binary": "airelay"}),
        "projects": projects,
    }


def expected_branches(gateway_id: str) -> set[str]:
    return {
        "main",
        f"gateway/{gateway_id}",
        *(f"project/{gateway_id}/{project_id}" for project_id in sorted(REQUIRED_PROJECTS)),
    }


def wait_ready(url: str, seconds: int = 60) -> None:
    deadline = time.time() + seconds
    last = ""
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as response:  # noqa: S310
                body = response.read().decode().strip()
                if response.status == 200 and body == "ready":
                    return
                last = f"HTTP {response.status}: {body}"
        except Exception as exc:  # noqa: BLE001
            last = str(exc)
        time.sleep(1)
    raise RuntimeError(f"gateway readiness did not become ready: {last}")


def verify_branch_trees(url: str, gateway_id: str, scratch: Path) -> None:
    check = scratch / "verify.git"
    run(["git", "clone", "--bare", url, str(check)])
    control = run(["git", "--git-dir", str(check), "ls-tree", "-r", "--name-only", f"refs/heads/gateway/{gateway_id}"]).stdout.splitlines()
    if control != ["gateway.json"]:
        raise RuntimeError(f"control branch tree is not exactly gateway.json: {control}")
    gateway_payload = json.loads(run(["git", "--git-dir", str(check), "show", f"refs/heads/gateway/{gateway_id}:gateway.json"]).stdout)
    if gateway_payload.get("gateway_id") != gateway_id:
        raise RuntimeError("gateway.json gateway_id mismatch")
    project_ids = {item.get("project_id") for item in gateway_payload.get("projects", [])}
    if project_ids != set(REQUIRED_PROJECTS):
        raise RuntimeError(f"gateway.json project list mismatch: {sorted(project_ids)}")
    required = {
        "README.md", "PROJECT_CONTEXT.md", "project.json", "state/checkpoint.json",
        "inbox/.gitkeep", "results/.gitkeep", "archive/.gitkeep",
    }
    for project_id in REQUIRED_PROJECTS:
        branch = f"refs/heads/project/{gateway_id}/{project_id}"
        tree = set(run(["git", "--git-dir", str(check), "ls-tree", "-r", "--name-only", branch]).stdout.splitlines())
        if tree != required:
            raise RuntimeError(f"project branch {branch} tree mismatch: {sorted(tree)}")


def execute(args: argparse.Namespace, config: dict[str, Any], source_root: Path) -> dict[str, Any]:
    gateway_id = config["gateway"]["id"]
    home = Path.home()
    gateway_root = home / ".gpt-github-gateway" / gateway_id
    source_info = preflight_source(source_root)
    blocking = scan_active_tasks(gateway_root, config["projects"].keys())
    if blocking:
        raise RuntimeError("non-terminal gateway tasks exist: " + "; ".join(f"{p}: {s}" for p, s in blocking))

    lock = home / ".gpt-github-gateway" / ".multibranch-migration.lock"
    try:
        lock.mkdir(parents=True)
    except FileExistsError as exc:
        raise RuntimeError(f"migration lock already exists: {lock}") from exc

    report: dict[str, Any] = {"schema_version": 1, "started_at": datetime.now(timezone.utc).isoformat(), "gateway_id": gateway_id, **source_info}
    backup: Path | None = None
    stage = "backup"
    try:
        backup = home / ".gpt-github-gateway" / "backups" / f"typer-multibranch-{utc_stamp()}"
        backup.mkdir(parents=True)
        report["backup_directory"] = str(backup)
        url = config["bus"]["url"]
        branches, tags = list_remote_refs(url)
        report["initial_branches"] = branches
        report["initial_tags"] = tags
        if "main" not in branches:
            raise RuntimeError("remote main branch is missing")
        old_main = branches["main"]

        mirror = backup / "mirror.git"
        run(["git", "clone", "--mirror", url, str(mirror)])
        bundle = backup / "rceman-typer-before-migration.bundle"
        run(["git", "-C", str(mirror), "bundle", "create", str(bundle), "--all"])
        verify = run(["git", "bundle", "verify", str(bundle)])
        bundle_sha = sha256_file(bundle)
        (bundle.with_suffix(bundle.suffix + ".sha256")).write_text(f"{bundle_sha}  {bundle.name}\n", encoding="utf-8")
        report["bundle"] = str(bundle)
        report["bundle_sha256"] = bundle_sha
        report["bundle_verification"] = (verify.stdout + verify.stderr).strip()

        config_path = Path(args.config).resolve()
        shutil.copy2(config_path, backup / "gateway-config-before-migration.json")
        gateway_bin = home / ".local" / "bin" / "gpt-github-gateway"
        if gateway_bin.exists():
            shutil.copy2(gateway_bin, backup / "gateway-binary-before-migration")

        stage = "stable-machine-code"
        run([str(gateway_bin), "--config", str(config_path), "stop"])

        run(["bash", str(source_root / "scripts" / "install.sh")], cwd=source_root)
        verify_binary_version(gateway_bin)

        stage = "remote-main"
        with tempfile.TemporaryDirectory(prefix="typer-main-reset-") as temp_name:
            temp = Path(temp_name)
            repo = temp / "repo"
            run(["git", "clone", url, str(repo)])
            run(["git", "-C", str(repo), "config", "user.name", "gpt-github-gateway migration"])
            run(["git", "-C", str(repo), "config", "user.email", "gateway@localhost.invalid"])
            run(["git", "-C", str(repo), "checkout", "--orphan", "bus-main-reset"])
            run(["git", "-C", str(repo), "rm", "-rf", "--ignore-unmatch", "."])
            for child in repo.iterdir():
                if child.name != ".git":
                    if child.is_dir(): shutil.rmtree(child)
                    else: child.unlink()
            for name, content in new_main_files().items():
                path = repo / name
                path.write_text(content, encoding="utf-8")
            run(["git", "-C", str(repo), "add", "--all"])
            run(["git", "-C", str(repo), "commit", "-m", "chore: initialize multi-branch gateway bus"])
            new_main = run(["git", "-C", str(repo), "rev-parse", "HEAD"]).stdout.strip()
            parents = run(["git", "-C", str(repo), "rev-list", "--parents", "-n", "1", "HEAD"]).stdout.split()
            if len(parents) != 1:
                raise RuntimeError("new main is not a root commit")
            run(["git", "-C", str(repo), "push", f"--force-with-lease=refs/heads/main:{old_main}", "origin", f"{new_main}:refs/heads/main"])
            report["new_main"] = new_main

        airelay_path = Path(args.airelay_path).expanduser().resolve()
        if not (airelay_path / ".git").exists():
            airelay_path.parent.mkdir(parents=True, exist_ok=True)
            run(["git", "clone", "git@github.com:therceman/airelay.git", str(airelay_path)])
        stage = "configuration"
        converted = convert_config(config, airelay_path)
        atomic_json(config_path, converted)

        old_bus = gateway_root / "bus"
        if old_bus.exists():
            shutil.move(str(old_bus), str(backup / "local-bus-before-migration"))

        stage = "gateway-runtime"
        run([str(gateway_bin), "--config", str(config_path), "start"])
        wait_ready("http://127.0.0.1:8787/readyz")

        expected = expected_branches(gateway_id)
        deadline = time.time() + 60
        while time.time() < deadline:
            current, _ = list_remote_refs(url)
            if expected.issubset(current):
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"gateway did not bootstrap expected branches: {sorted(expected - set(current))}")

        with tempfile.TemporaryDirectory(prefix="typer-verify-") as verify_temp:
            verify_branch_trees(url, gateway_id, Path(verify_temp))

        stage = "legacy-ref-cleanup"
        current, current_tags = list_remote_refs(url)
        deleted_branches: list[str] = []
        for branch in sorted(set(current) - expected):
            delete_remote_ref(url, "refs/heads/" + branch)
            deleted_branches.append(branch)
        deleted_tags: list[str] = []
        for tag in sorted(current_tags):
            delete_remote_ref(url, "refs/tags/" + tag)
            deleted_tags.append(tag)

        final_branches, final_tags = list_remote_refs(url)
        if set(final_branches) != expected or final_tags:
            raise RuntimeError(f"final ref allowlist mismatch branches={sorted(final_branches)} tags={sorted(final_tags)}")
        report.update({
            "deleted_branches": deleted_branches,
            "deleted_tags": deleted_tags,
            "final_branches": final_branches,
            "final_tags": final_tags,
            "config_schema_version": 2,
            "completed_at": datetime.now(timezone.utc).isoformat(),
            "status": "completed",
        })
        atomic_json(backup / "migration-report.json", report)
        return report
    except Exception as exc:
        if backup is not None and backup.exists():
            report.update({"status": "failed", "failed_stage": stage, "error": str(exc)[:500], "failed_at": datetime.now(timezone.utc).isoformat()})
            atomic_json(backup / "migration-report.json", report)
        raise
    finally:
        shutil.rmtree(lock, ignore_errors=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--source-root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--airelay-path", default=str(Path.home() / ".gpt-github-gateway" / "sources" / "airelay"))
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true")
    mode.add_argument("--execute", action="store_true")
    parser.add_argument("--confirm-repository")
    args = parser.parse_args()

    config_path = Path(args.config).expanduser().resolve()
    config = json.loads(config_path.read_text(encoding="utf-8"))
    if config.get("bus", {}).get("repository") != "rceman/typer":
        raise SystemExit("unexpected bus.repository")
    source_root = Path(args.source_root).expanduser().resolve()
    branches, tags = list_remote_refs(config["bus"]["url"])
    plan = {
        "schema_version": 1,
        "mode": "execute" if args.execute else "dry-run",
        "repository": "rceman/typer",
        "gateway_id": config.get("gateway", {}).get("id"),
        "current_schema_version": config.get("schema_version"),
        "current_branches": branches,
        "current_tags": tags,
        "target_branches": sorted(expected_branches(config["gateway"]["id"])),
        "source_root": str(source_root),
    }
    if not args.execute:
        print(json.dumps(plan, indent=2))
        return 0
    if args.confirm_repository != "rceman/typer":
        raise SystemExit("--execute requires --confirm-repository rceman/typer")
    result = execute(args, config, source_root)
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # noqa: BLE001
        print(f"migration failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
