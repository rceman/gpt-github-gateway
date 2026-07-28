#!/usr/bin/env python3
"""Build one deterministic atomic task bundle for the Git bus."""
from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path, PurePosixPath
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
VALIDATOR = ROOT / "scripts/validate-task-bundle.py"
DEFAULT_MAX_JSON_BYTES = 950_000


class BuildError(RuntimeError):
    pass


def load_object(path: Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise BuildError(f"invalid {label}: {exc}") from exc
    if not isinstance(value, dict):
        raise BuildError(f"{label} root must be an object")
    return value


def git(planner: Path, *args: str) -> bytes:
    result = subprocess.run(
        ["git", "-C", str(planner), *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if result.returncode != 0:
        message = result.stderr.decode("utf-8", errors="replace").strip()
        raise BuildError(message or f"git {' '.join(args)} failed")
    return result.stdout


def materialize_planner_tools(planner: Path, commit: str, destination: Path) -> None:
    git(planner, "cat-file", "-e", f"{commit}^{{commit}}")
    names = (
        "validate-patch-pack.py",
        "patch_pack_scope.py",
        "verify-agent-evidence.py",
        "validate-patch-pack-delivery.py",
    )
    destination.mkdir(parents=True)
    for name in names:
        source = f"scripts/{name}"
        result = subprocess.run(
            ["git", "-C", str(planner), "show", f"{commit}:{source}"],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        if result.returncode != 0:
            if name == "validate-patch-pack-delivery.py":
                continue
            message = result.stderr.decode("utf-8", errors="replace").strip()
            raise BuildError(f"pinned planner is missing {source}: {message}")
        target = destination / name
        target.write_bytes(result.stdout)
        target.chmod(0o700)


def run_planner_validation(pack: Path, planner: Path, workflow: dict[str, Any]) -> None:
    commit = workflow.get("commit")
    if not isinstance(commit, str) or len(commit) != 40:
        raise BuildError("manifest.workflow.commit must be a 40-character SHA")
    with tempfile.TemporaryDirectory(prefix="gpt-planner-tools-") as temp_name:
        planner_view = Path(temp_name) / "planner"
        tools = planner_view / "scripts"
        materialize_planner_tools(planner, commit, tools)
        result = subprocess.run(
            [sys.executable, str(tools / "validate-patch-pack.py"), str(pack)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise BuildError("planner patch-pack validation failed:\n" + (result.stderr or result.stdout).rstrip())
        delivery = tools / "validate-patch-pack-delivery.py"
        if delivery.is_file():
            manifest = load_object(pack / "manifest.json", "manifest.json")
            patch_id = manifest.get("patch_id")
            archive_name = f"{patch_id}.tar.gz"
            result = subprocess.run(
                [
                    sys.executable,
                    str(delivery),
                    "--pack-root",
                    str(pack),
                    "--planner-root",
                    str(planner_view),
                    "--archive-name",
                    archive_name,
                    "--sidecar-name",
                    archive_name + ".sha256",
                ],
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                raise BuildError(
                    "planner patch-pack delivery validation failed:\n"
                    + (result.stderr or result.stdout).rstrip()
                )


def safe_pack_files(root: Path) -> list[Path]:
    files: list[Path] = []
    for path in root.rglob("*"):
        relative = path.relative_to(root).as_posix()
        pure = PurePosixPath(relative)
        if pure.is_absolute() or any(part in {"", ".", ".."} for part in pure.parts):
            raise BuildError(f"unsafe patch-pack path: {relative}")
        info = path.lstat()
        if path.is_symlink():
            raise BuildError(f"patch pack contains symlink: {relative}")
        if path.is_dir():
            continue
        if not path.is_file():
            raise BuildError(f"patch pack contains non-regular file: {relative}")
        files.append(path)
    files.sort(key=lambda item: item.relative_to(root).as_posix().encode("utf-8"))
    return files


def deterministic_archive(root: Path) -> tuple[bytes, int, int]:
    files = safe_pack_files(root)
    if not files:
        raise BuildError("patch pack is empty")
    raw = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w|") as tar:
            total = 0
            for file in files:
                relative = file.relative_to(root).as_posix()
                data = file.read_bytes()
                total += len(data)
                info = tarfile.TarInfo(relative)
                info.size = len(data)
                info.mode = 0o755 if file.stat().st_mode & 0o111 else 0o644
                info.uid = 0
                info.gid = 0
                info.uname = ""
                info.gname = ""
                info.mtime = 0
                tar.addfile(info, io.BytesIO(data))
    return raw.getvalue(), total, len(files)


def load_validator_module():
    spec = importlib.util.spec_from_file_location("validate_task_bundle", VALIDATOR)
    if spec is None or spec.loader is None:
        raise BuildError("cannot load task-bundle validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def build(args: argparse.Namespace) -> dict[str, Any]:
    spec = load_object(args.task_spec.resolve(), "task spec")
    allowed = {
        "schema_version",
        "task_id",
        "gateway_id",
        "project_id",
        "submitted_at",
        "result_branch",
        "task",
    }
    unknown = set(spec) - allowed
    missing = {"schema_version", "task_id", "gateway_id", "project_id", "submitted_at", "task"} - set(spec)
    if unknown:
        raise BuildError("task spec has unknown fields: " + ", ".join(sorted(unknown)))
    if missing:
        raise BuildError("task spec is missing fields: " + ", ".join(sorted(missing)))
    if spec["schema_version"] != 1:
        raise BuildError("task spec schema_version must be 1")

    pack = args.pack_root.resolve()
    manifest = load_object(pack / "manifest.json", "manifest.json")
    workflow = manifest.get("workflow")
    if not isinstance(workflow, dict):
        raise BuildError("manifest.workflow must be an object")
    run_planner_validation(pack, args.planner_root.resolve(), workflow)

    archive, uncompressed, entries = deterministic_archive(pack)
    digest = hashlib.sha256(archive).hexdigest()
    task_id = spec["task_id"]
    result_branch = spec.get("result_branch") or f"agent/{task_id}"
    bundle = {
        "schema_version": 2,
        "task_id": task_id,
        "gateway_id": spec["gateway_id"],
        "project_id": spec["project_id"],
        "operation": "apply_patch_pack",
        "submitted_at": spec["submitted_at"],
        "result_branch": result_branch,
        "task": spec["task"],
        "archive": {
            "format": "tar-gzip",
            "encoding": "base64",
            "sha256": digest,
            "compressed_size_bytes": len(archive),
            "uncompressed_size_bytes": uncompressed,
            "entry_count": entries,
            "content": base64.b64encode(archive).decode("ascii"),
        },
    }
    validator = load_validator_module()
    validator.validate_bundle(bundle)
    return bundle


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--planner-root", type=Path, required=True)
    parser.add_argument("--pack-root", type=Path, required=True)
    parser.add_argument("--task-spec", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--max-json-bytes", type=int, default=DEFAULT_MAX_JSON_BYTES)
    args = parser.parse_args()
    try:
        bundle = build(args)
        rendered = json.dumps(
            bundle, ensure_ascii=False, sort_keys=True, separators=(",", ":")
        ).encode("utf-8") + b"\n"
        if len(rendered) > args.max_json_bytes:
            raise BuildError(
                f"task bundle is {len(rendered)} bytes, exceeding the configured one-file GitHub publication limit "
                f"of {args.max_json_bytes} bytes"
            )
        output = args.output.resolve()
        output.parent.mkdir(parents=True, exist_ok=True)
        temporary = output.with_name(output.name + ".tmp")
        temporary.write_bytes(rendered)
        validator = load_validator_module()
        validator.validate_bundle(validator.load_json(temporary))
        os.replace(temporary, output)
    except (BuildError, OSError, ValueError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    print("TASK_BUNDLE_VALID")
    print(f"task_id={bundle['task_id']}")
    print(f"bundle_sha256={bundle['archive']['sha256']}")
    print(f"compressed_bytes={bundle['archive']['compressed_size_bytes']}")
    print(f"uncompressed_bytes={bundle['archive']['uncompressed_size_bytes']}")
    print(f"output={args.output.resolve()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
