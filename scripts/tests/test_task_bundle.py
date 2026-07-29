from __future__ import annotations

import base64
import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def load_module(name: str, relative: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


builder = load_module("build_task_bundle", "scripts/build-task-bundle.py")
validator = load_module("validate_task_bundle", "scripts/validate-task-bundle.py")


class TaskBundleTests(unittest.TestCase):
    # Protocol-v2 bundle coverage remains part of the multi-branch transport gate.
    def pack(self, root: Path) -> Path:
        pack = root / "pack"
        (pack / "patch").mkdir(parents=True)
        (pack / "AGENT_HANDOFF.md").write_text("# AGENT_HANDOFF\n", encoding="utf-8")
        (pack / "manifest.json").write_text("{}\n", encoding="utf-8")
        (pack / "patch/changes.patch").write_text("patch\n", encoding="utf-8")
        return pack

    def bundle(self, pack: Path) -> dict[str, object]:
        archive, total, entries = builder.deterministic_archive(pack)
        return {
            "schema_version": 2,
            "task_id": "task_001",
            "gateway_id": "home_pc",
            "project_id": "gpt-github-gateway",
            "operation": "apply_patch_pack",
            "submitted_at": "2026-07-28T12:00:00Z",
            "result_branch": "agent/task_001",
            "task": {
                "title": "Apply atomic task bundle",
                "summary": "Apply and validate the supplied implementation.",
                "objectives": ["Apply the patch pack."],
                "constraints": ["Do not broaden scope."],
                "acceptance_criteria": ["All required gates pass."],
            },
            "archive": {
                "format": "tar-gzip",
                "encoding": "base64",
                "sha256": hashlib.sha256(archive).hexdigest(),
                "compressed_size_bytes": len(archive),
                "uncompressed_size_bytes": total,
                "entry_count": entries,
                "content": base64.b64encode(archive).decode("ascii"),
            },
        }

    def test_deterministic_archive_and_validation(self) -> None:
        with tempfile.TemporaryDirectory() as temp_name:
            pack = self.pack(Path(temp_name))
            first = builder.deterministic_archive(pack)
            second = builder.deterministic_archive(pack)
            self.assertEqual(first, second)
            summary = validator.validate_bundle(self.bundle(pack))
            self.assertEqual(summary["task_id"], "task_001")

    def test_rejects_sha_mismatch_and_unknown_fields(self) -> None:
        with tempfile.TemporaryDirectory() as temp_name:
            root = Path(temp_name)
            bundle = self.bundle(self.pack(root / "sha-mismatch"))
            bundle["archive"]["sha256"] = "0" * 64
            with self.assertRaises(validator.ValidationError):
                validator.validate_bundle(bundle)
            bundle = self.bundle(self.pack(root / "unknown-field"))
            bundle["unexpected"] = True
            with self.assertRaises(validator.ValidationError):
                validator.validate_bundle(bundle)

    def test_rejects_incomplete_structured_task(self) -> None:
        with tempfile.TemporaryDirectory() as temp_name:
            bundle = self.bundle(self.pack(Path(temp_name)))
            del bundle["task"]["acceptance_criteria"]
            with self.assertRaises(validator.ValidationError):
                validator.validate_bundle(bundle)

    def test_emitted_json_round_trip(self) -> None:
        with tempfile.TemporaryDirectory() as temp_name:
            root = Path(temp_name)
            bundle = self.bundle(self.pack(root))
            path = root / "task.taskbundle.json"
            path.write_text(json.dumps(bundle), encoding="utf-8")
            loaded = validator.load_json(path)
            self.assertEqual(validator.validate_bundle(loaded)["project_id"], "gpt-github-gateway")


if __name__ == "__main__":
    unittest.main()
