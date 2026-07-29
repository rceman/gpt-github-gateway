import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[1] / "migrate-bus-multibranch.py"


def load_module():
    spec = importlib.util.spec_from_file_location("migrate_bus_multibranch", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader
    spec.loader.exec_module(module)
    return module


class MigrationTests(unittest.TestCase):
    def test_scan_active_tasks_ignores_archived_status(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            root = Path(tmp_name)
            (root / "bus/archive/status.json").parent.mkdir(parents=True)
            (root / "bus/archive/status.json").write_text('{"state":"waiting_for_approval"}')
            self.assertEqual(module.scan_active_tasks(root, ["project"]), [])

    def test_scan_active_tasks_ignores_bus_worktree_status(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            root = Path(tmp_name)
            path = root / "project/tasks/task/bus/worktree/status.json"
            path.parent.mkdir(parents=True)
            path.write_text('{"state":"waiting_for_approval"}')
            self.assertEqual(module.scan_active_tasks(root, ["project"]), [])

    def test_scan_active_tasks_detects_canonical_nonterminal_task(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            path = Path(tmp_name) / "project/tasks/task/status.json"
            path.parent.mkdir(parents=True)
            path.write_text('{"state":"waiting_for_approval"}')
            self.assertEqual(len(module.scan_active_tasks(path.parents[3], ["project"])), 1)

    def test_scan_active_tasks_accepts_canonical_terminal_task(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            path = Path(tmp_name) / "project/tasks/task/status.json"
            path.parent.mkdir(parents=True)
            path.write_text('{"state":"completed"}')
            self.assertEqual(module.scan_active_tasks(path.parents[3], ["project"]), [])

    def test_scan_active_tasks_rejects_malformed_canonical_status(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            path = Path(tmp_name) / "project/tasks/task/status.json"
            path.parent.mkdir(parents=True)
            path.write_text("not json")
            result = module.scan_active_tasks(path.parents[3], ["project"])
            self.assertIn("invalid status JSON", result[0][1])

    def test_delete_remote_branch_from_disposable_bare_remote(self):
        module, remote, mirror, bundle = self._ref_fixture()
        module.delete_remote_ref(str(remote), "refs/heads/obsolete")
        refs = subprocess.check_output(["git", "--git-dir", str(remote), "show-ref"], text=True)
        self.assertNotIn("refs/heads/obsolete", refs)
        self.assertIn("refs/heads/main", refs)
        subprocess.run(["git", "bundle", "verify", str(bundle)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.assertIn("refs/heads/obsolete", subprocess.check_output(["git", "--git-dir", str(mirror), "show-ref"], text=True))

    def test_delete_remote_tag_from_disposable_bare_remote(self):
        module, remote, _mirror, _bundle = self._ref_fixture()
        module.delete_remote_ref(str(remote), "refs/tags/obsolete-tag")
        refs = subprocess.check_output(["git", "--git-dir", str(remote), "show-ref"], text=True)
        self.assertNotIn("refs/tags/obsolete-tag", refs)

    def test_delete_remote_ref_does_not_modify_backup_mirror(self):
        module, remote, mirror, _bundle = self._ref_fixture()
        before = subprocess.check_output(["git", "--git-dir", str(mirror), "show-ref"], text=True)
        module.delete_remote_ref(str(remote), "refs/heads/obsolete")
        self.assertEqual(before, subprocess.check_output(["git", "--git-dir", str(mirror), "show-ref"], text=True))

    def _ref_fixture(self):
        module = load_module()
        root = Path(tempfile.mkdtemp())
        work = root / "work"
        remote = root / "remote.git"
        subprocess.run(["git", "init", "-b", "main", str(work)], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(["git", "-C", str(work), "config", "user.name", "test"], check=True)
        subprocess.run(["git", "-C", str(work), "config", "user.email", "test@example.invalid"], check=True)
        (work / "README.md").write_text("root\n")
        subprocess.run(["git", "-C", str(work), "add", "README.md"], check=True)
        subprocess.run(["git", "-C", str(work), "commit", "-m", "root"], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(["git", "-C", str(work), "branch", "obsolete"], check=True)
        subprocess.run(["git", "-C", str(work), "tag", "obsolete-tag"], check=True)
        subprocess.run(["git", "clone", "--bare", str(work), str(remote)], check=True, stdout=subprocess.DEVNULL)
        mirror = root / "mirror.git"
        subprocess.run(["git", "clone", "--mirror", str(remote), str(mirror)], check=True, stdout=subprocess.DEVNULL)
        bundle = root / "backup.bundle"
        subprocess.run(["git", "-C", str(mirror), "bundle", "create", str(bundle), "--all"], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(["git", "bundle", "verify", str(bundle)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return module, remote, mirror, bundle

    def test_source_preflight_rejects_wrong_version(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            root = Path(tmp_name); (root / ".git").mkdir(); (root / "VERSION").write_text("0.2.0\n")
            with self.assertRaisesRegex(RuntimeError, "source version"):
                module.preflight_source(root)

    def test_source_preflight_runs_before_remote_mutation(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as tmp_name:
            root = Path(tmp_name); (root / ".git").mkdir(); (root / "VERSION").write_text("0.2.0\n")
            with mock.patch.object(module, "run") as command:
                with self.assertRaises(RuntimeError): module.preflight_source(root)
                command.assert_not_called()

    def test_installed_binary_version_checked_before_main_reset(self):
        module = load_module()
        with mock.patch.object(module, "run", return_value=subprocess.CompletedProcess([], 0, "0.2.0\n", "")):
            with self.assertRaisesRegex(RuntimeError, "installed binary version"):
                module.verify_binary_version(Path("/tmp/gateway"))

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
