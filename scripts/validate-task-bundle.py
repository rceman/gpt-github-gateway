#!/usr/bin/env python3
"""Validate one atomic protocol-v2 task bundle without extracting it to disk."""
from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import io
import json
import re
import sys
import tarfile
from datetime import datetime
from pathlib import Path, PurePosixPath
from typing import Any

SCHEMA_VERSION = 2
ARCHIVE_FORMAT = "tar-gzip"
ARCHIVE_ENCODING = "base64"
SLUG_RE = re.compile(r"^[a-z0-9][a-z0-9_-]{0,79}$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
REQUIRED_PACK_FILES = {"AGENT_HANDOFF.md", "manifest.json"}
DEFAULT_MAX_ARCHIVE_BYTES = 100 << 20
DEFAULT_MAX_FILE_BYTES = 20 << 20
DEFAULT_MAX_ENTRIES = 10_000


class ValidationError(ValueError):
    pass


def strict_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValidationError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=strict_object)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValidationError(f"invalid task bundle JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise ValidationError("task bundle root must be an object")
    return value


def require_keys(value: dict[str, Any], expected: set[str], label: str) -> None:
    unknown = set(value) - expected
    missing = expected - set(value)
    if unknown:
        raise ValidationError(f"{label} has unknown fields: {', '.join(sorted(unknown))}")
    if missing:
        raise ValidationError(f"{label} is missing fields: {', '.join(sorted(missing))}")


def safe_archive_path(raw: str) -> str:
    if not raw or "\\" in raw or "\x00" in raw or "\r" in raw or "\n" in raw:
        raise ValidationError(f"unsafe archive path: {raw!r}")
    path = PurePosixPath(raw)
    if path.is_absolute() or raw != path.as_posix() or any(part in {"", ".", ".."} for part in path.parts):
        raise ValidationError(f"unsafe archive path: {raw!r}")
    return raw


def validate_archive(
    archive: dict[str, Any],
    *,
    max_archive_bytes: int,
    max_file_bytes: int,
    max_entries: int,
) -> dict[str, Any]:
    require_keys(
        archive,
        {
            "format",
            "encoding",
            "sha256",
            "compressed_size_bytes",
            "uncompressed_size_bytes",
            "entry_count",
            "content",
        },
        "archive",
    )
    if archive["format"] != ARCHIVE_FORMAT or archive["encoding"] != ARCHIVE_ENCODING:
        raise ValidationError("archive must use tar-gzip with base64 encoding")
    digest = archive["sha256"]
    if not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
        raise ValidationError("archive.sha256 must be a lowercase SHA-256")
    for name in ("compressed_size_bytes", "uncompressed_size_bytes", "entry_count"):
        if not isinstance(archive[name], int) or isinstance(archive[name], bool) or archive[name] <= 0:
            raise ValidationError(f"archive.{name} must be a positive integer")
    if archive["compressed_size_bytes"] > max_archive_bytes:
        raise ValidationError("archive compressed size exceeds limit")
    if archive["uncompressed_size_bytes"] > max_archive_bytes:
        raise ValidationError("archive uncompressed size exceeds limit")
    if archive["entry_count"] > max_entries:
        raise ValidationError("archive entry count exceeds limit")
    if not isinstance(archive["content"], str):
        raise ValidationError("archive.content must be a base64 string")
    try:
        compressed = base64.b64decode(archive["content"], validate=True)
    except ValueError as exc:
        raise ValidationError(f"invalid archive base64: {exc}") from exc
    if len(compressed) != archive["compressed_size_bytes"]:
        raise ValidationError("archive compressed size mismatch")
    if hashlib.sha256(compressed).hexdigest() != digest:
        raise ValidationError("archive SHA-256 mismatch")

    names: set[str] = set()
    folded: dict[str, str] = {}
    total = 0
    entries = 0
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(compressed), mode="rb") as gz:
            with tarfile.open(fileobj=gz, mode="r|") as tar:
                for member in tar:
                    name = safe_archive_path(member.name)
                    lower = name.casefold()
                    if name in names:
                        raise ValidationError(f"duplicate archive path: {name}")
                    if lower in folded and folded[lower] != name:
                        raise ValidationError(
                            f"case-folding archive collision: {folded[lower]} and {name}"
                        )
                    names.add(name)
                    folded[lower] = name
                    if not member.isreg():
                        raise ValidationError(f"unsupported archive entry type: {name}")
                    if member.uid != 0 or member.gid != 0 or int(member.mtime) != 0:
                        raise ValidationError(f"non-deterministic archive metadata: {name}")
                    if member.size < 0 or member.size > max_file_bytes:
                        raise ValidationError(f"archive file exceeds limit: {name}")
                    entries += 1
                    total += member.size
                    if total > max_archive_bytes:
                        raise ValidationError("archive aggregate size exceeds limit")
                    handle = tar.extractfile(member)
                    if handle is None:
                        raise ValidationError(f"cannot read archive file: {name}")
                    read = 0
                    while True:
                        chunk = handle.read(1024 * 1024)
                        if not chunk:
                            break
                        read += len(chunk)
                    if read != member.size:
                        raise ValidationError(f"archive member size mismatch: {name}")
    except (gzip.BadGzipFile, tarfile.TarError, EOFError, OSError) as exc:
        raise ValidationError(f"invalid tar.gz archive: {exc}") from exc

    if entries != archive["entry_count"]:
        raise ValidationError("archive entry_count mismatch")
    if total != archive["uncompressed_size_bytes"]:
        raise ValidationError("archive uncompressed size mismatch")
    missing = REQUIRED_PACK_FILES - names
    if missing:
        raise ValidationError("archive is missing required files: " + ", ".join(sorted(missing)))
    return {
        "sha256": digest,
        "compressed_size_bytes": len(compressed),
        "uncompressed_size_bytes": total,
        "entry_count": entries,
    }


def validate_string_items(value: Any, label: str, *, required: bool) -> None:
    if not isinstance(value, list):
        raise ValidationError(f"task.{label} must be an array")
    if required and not value:
        raise ValidationError(f"task.{label} must be a non-empty array")
    if len(value) > 64:
        raise ValidationError(f"task.{label} may contain at most 64 items")
    for index, item in enumerate(value):
        if not isinstance(item, str) or not item.strip() or len(item.encode("utf-8")) > 2048:
            raise ValidationError(
                f"task.{label}[{index}] must contain 1 to 2048 UTF-8 bytes"
            )


def validate_task_document(value: Any) -> None:
    if not isinstance(value, dict):
        raise ValidationError("task must be an object")
    required = {"title", "summary", "objectives", "constraints", "acceptance_criteria"}
    allowed = required | {"references"}
    unknown = set(value) - allowed
    missing = required - set(value)
    if unknown:
        raise ValidationError("task has unknown fields: " + ", ".join(sorted(unknown)))
    if missing:
        raise ValidationError("task is missing fields: " + ", ".join(sorted(missing)))
    title = value["title"]
    summary = value["summary"]
    if not isinstance(title, str) or not title.strip() or len(title.encode("utf-8")) > 160:
        raise ValidationError("task.title must contain 1 to 160 UTF-8 bytes")
    if not isinstance(summary, str) or not summary.strip() or len(summary.encode("utf-8")) > 4096:
        raise ValidationError("task.summary must contain 1 to 4096 UTF-8 bytes")
    validate_string_items(value["objectives"], "objectives", required=True)
    validate_string_items(value["constraints"], "constraints", required=True)
    validate_string_items(value["acceptance_criteria"], "acceptance_criteria", required=True)
    validate_string_items(value.get("references", []), "references", required=False)
    encoded = json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if len(encoded) > 256 << 10:
        raise ValidationError("structured task document exceeds 262144 bytes")


def validate_bundle(
    value: dict[str, Any],
    *,
    max_archive_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES,
    max_file_bytes: int = DEFAULT_MAX_FILE_BYTES,
    max_entries: int = DEFAULT_MAX_ENTRIES,
) -> dict[str, Any]:
    require_keys(
        value,
        {
            "schema_version",
            "task_id",
            "gateway_id",
            "project_id",
            "operation",
            "submitted_at",
            "result_branch",
            "task",
            "archive",
        },
        "task bundle",
    )
    if value["schema_version"] != SCHEMA_VERSION:
        raise ValidationError(f"schema_version must be {SCHEMA_VERSION}")
    for name in ("task_id", "gateway_id", "project_id"):
        if not isinstance(value[name], str) or not SLUG_RE.fullmatch(value[name]):
            raise ValidationError(f"{name} must be a safe lowercase slug")
    if value["operation"] != "apply_patch_pack":
        raise ValidationError("operation must be apply_patch_pack")
    if not isinstance(value["submitted_at"], str) or not value["submitted_at"].endswith("Z"):
        raise ValidationError("submitted_at must be an RFC3339 UTC timestamp")
    try:
        datetime.fromisoformat(value["submitted_at"].replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValidationError("submitted_at must be an RFC3339 UTC timestamp") from exc
    branch = value["result_branch"]
    if not isinstance(branch, str) or not branch.startswith("agent/") or any(
        character in branch for character in "\x00\r\n ~^:?*[]\\"
    ):
        raise ValidationError("result_branch must be a safe agent/ branch")
    task = value["task"]
    validate_task_document(task)
    archive = value["archive"]
    if not isinstance(archive, dict):
        raise ValidationError("archive must be an object")
    summary = validate_archive(
        archive,
        max_archive_bytes=max_archive_bytes,
        max_file_bytes=max_file_bytes,
        max_entries=max_entries,
    )
    return {
        "task_id": value["task_id"],
        "gateway_id": value["gateway_id"],
        "project_id": value["project_id"],
        **summary,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--max-archive-bytes", type=int, default=DEFAULT_MAX_ARCHIVE_BYTES)
    parser.add_argument("--max-file-bytes", type=int, default=DEFAULT_MAX_FILE_BYTES)
    parser.add_argument("--max-entries", type=int, default=DEFAULT_MAX_ENTRIES)
    args = parser.parse_args()
    try:
        summary = validate_bundle(
            load_json(args.bundle),
            max_archive_bytes=args.max_archive_bytes,
            max_file_bytes=args.max_file_bytes,
            max_entries=args.max_entries,
        )
    except ValidationError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    print("TASK_BUNDLE_VALID")
    for key, value in summary.items():
        print(f"{key}={value}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
