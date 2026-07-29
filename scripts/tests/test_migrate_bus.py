import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "migrate-bus-multibranch.py"


def load_module():
    spec = importlib.util.spec_from_file_location("migrate_bus_multibranch", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader
    spec.loader.exec_module(module)
    return module


class MigrationTests(unittest.TestCase):
    def test_convert_config_preserves_projects_and_adds_airelay(self):
        module = load_module()
        old = {
            "schema_version": 1,
            "gateway": {"id": "home_pc", "poll_interval_seconds": 10},
            "bus": {"repository": "rceman/typer", "url": "/tmp/remote.git", "branch": "ai-workspace-bus"},
            "server": {"listen": "127.0.0.1:8787"},
            "airelay": {"binary": "airelay"},
            "projects": {
                "gpt-github-gateway": {"path": "/tmp/gateway", "repository": "rceman/gpt-github-gateway", "default_branch": "main", "session_key": "gpt-github-gateway_master"},
                "gpt-review-planner": {"path": "/tmp/planner", "repository": "rceman/gpt-review-planner", "default_branch": "main", "session_key": "gpt-review-planner_master"},
            },
        }
        converted = module.convert_config(old, Path("/tmp/airelay"))
        self.assertEqual(converted["schema_version"], 2)
        self.assertNotIn("branch", converted["bus"])
        self.assertEqual(converted["projects"]["airelay"]["repository"], "therceman/airelay")
        self.assertEqual(converted["projects"]["airelay"]["default_branch"], "master")

    def test_dry_run_does_not_mutate_remote(self):
        with tempfile.TemporaryDirectory() as tmp_name:
            root = Path(tmp_name)
            work = root / "work"
            remote = root / "remote.git"
            subprocess.run(["git", "init", "-b", "main", str(work)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["git", "-C", str(work), "config", "user.name", "test"], check=True)
            subprocess.run(["git", "-C", str(work), "config", "user.email", "test@example.invalid"], check=True)
            (work / "README.md").write_text("legacy\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(work), "add", "README.md"], check=True)
            subprocess.run(["git", "-C", str(work), "commit", "-m", "legacy"], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["git", "clone", "--bare", str(work), str(remote)], check=True, stdout=subprocess.DEVNULL)
            before = subprocess.check_output(["git", "--git-dir", str(remote), "show-ref"], text=True)
            config = {
                "schema_version": 1,
                "gateway": {"id": "home_pc"},
                "bus": {"repository": "rceman/typer", "url": str(remote), "branch": "ai-workspace-bus"},
                "projects": {},
            }
            config_path = root / "config.json"
            config_path.write_text(json.dumps(config), encoding="utf-8")
            result = subprocess.run(["python3", str(SCRIPT), "--config", str(config_path), "--dry-run"], check=True, text=True, stdout=subprocess.PIPE)
            payload = json.loads(result.stdout)
            self.assertEqual(payload["mode"], "dry-run")
            self.assertIn("main", payload["current_branches"])
            after = subprocess.check_output(["git", "--git-dir", str(remote), "show-ref"], text=True)
            self.assertEqual(before, after)

    def test_execute_requires_exact_confirmation(self):
        with tempfile.TemporaryDirectory() as tmp_name:
            root = Path(tmp_name)
            work = root / "work"
            remote = root / "remote.git"
            subprocess.run(["git", "init", "-b", "main", str(work)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["git", "-C", str(work), "config", "user.name", "test"], check=True)
            subprocess.run(["git", "-C", str(work), "config", "user.email", "test@example.invalid"], check=True)
            (work / "README.md").write_text("legacy\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(work), "add", "README.md"], check=True)
            subprocess.run(["git", "-C", str(work), "commit", "-m", "legacy"], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["git", "clone", "--bare", str(work), str(remote)], check=True, stdout=subprocess.DEVNULL)
            config_path = root / "config.json"
            config_path.write_text(json.dumps({"schema_version": 1, "gateway": {"id": "home_pc"}, "bus": {"repository": "rceman/typer", "url": str(remote)}, "projects": {}}), encoding="utf-8")
            result = subprocess.run(["python3", str(SCRIPT), "--config", str(config_path), "--execute"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("--confirm-repository", result.stderr)


if __name__ == "__main__":
    unittest.main()
