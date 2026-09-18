"""Unit tests for `wt-ctl wait` and `wt-ctl create --detach`.

`wait` polls the exit-code file a background run writes:
``.generated/wt-ctl/create.exit`` (create) or ``logs/test.exit`` (e2e_run.sh).
The detach spawn re-executes wt-ctl with the original argv minus ``--detach``
and streams the child's output into the worktree's setup log.
"""

from __future__ import annotations

import io
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)
from wt_ctl import cli  # noqa: E402


def _refs(anchor: Path):
    return types.SimpleNamespace(
        worktree_root=anchor,
        main_repo_root=anchor.parent,
        branch_dir=anchor.name,
    )


class WaitTests(unittest.TestCase):
    def test_timeout_returns_124(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            anchor = Path(td) / "anchor"
            anchor.mkdir()
            args = types.SimpleNamespace(branch=None, test=False, timeout=0.0, poll=0.01)
            with mock.patch("wt_ctl.cli.time.sleep"):
                rc = cli.cmd_wait(mock.Mock(), _refs(anchor), args)
            self.assertEqual(rc, 124)

    def test_create_exit_code_is_returned(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            anchor = Path(td) / "anchor"
            anchor.mkdir()
            artifacts = Path(td) / "artifacts"
            artifacts.mkdir()
            (artifacts / "anchor.exit").write_text("0\n")
            args = types.SimpleNamespace(branch=None, test=False, timeout=1.0, poll=0.01)
            with mock.patch("wt_ctl.cli.create_artifacts_dir", return_value=artifacts):
                self.assertEqual(cli.cmd_wait(mock.Mock(), _refs(anchor), args), 0)

    def test_failure_tails_the_log(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            anchor = Path(td) / "anchor"
            anchor.mkdir()
            artifacts = Path(td) / "artifacts"
            artifacts.mkdir()
            (artifacts / "anchor.exit").write_text("7\n")
            (artifacts / "anchor.log").write_text("line1\nboom\n")
            args = types.SimpleNamespace(branch=None, test=False, timeout=1.0, poll=0.01)
            stderr = io.StringIO()
            with (
                mock.patch("wt_ctl.cli.create_artifacts_dir", return_value=artifacts),
                mock.patch("sys.stderr", stderr),
            ):
                rc = cli.cmd_wait(mock.Mock(), _refs(anchor), args)
            self.assertEqual(rc, 7)
            self.assertIn("boom", stderr.getvalue())

    def test_test_flag_reads_test_exit(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            anchor = Path(td) / "anchor"
            (anchor / "logs").mkdir(parents=True)
            (anchor / "logs" / "test.exit").write_text("0\n")
            args = types.SimpleNamespace(branch=None, test=True, timeout=1.0, poll=0.01)
            self.assertEqual(cli.cmd_wait(mock.Mock(), _refs(anchor), args), 0)

    def test_branch_resolves_sibling_worktree(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            parent = Path(td)
            (parent / "anchor").mkdir()
            target = parent / "lsierant_x-y"
            (target / "logs").mkdir(parents=True)
            (target / "logs" / "test.exit").write_text("0\n")
            args = types.SimpleNamespace(branch="lsierant/x-y", test=True, timeout=1.0, poll=0.01)
            self.assertEqual(cli.cmd_wait(mock.Mock(), _refs(parent / "anchor"), args), 0)


class SpawnDetachedTests(unittest.TestCase):
    def test_spawn_detaches_and_prints_paths(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            parent = Path(td)
            (parent / "anchor").mkdir()
            artifacts = parent / "artifacts"
            artifacts.mkdir()
            args = types.SimpleNamespace(branch="lsierant/detach-me")
            runner = mock.Mock()
            runner.run_detached.return_value = 4321
            stderr = io.StringIO()
            with (
                mock.patch("wt_ctl.cli.create_artifacts_dir", return_value=artifacts),
                mock.patch("sys.stderr", stderr),
            ):
                rc = cli._spawn_detached_create(
                    runner, _refs(parent / "anchor"), args, ["create", "lsierant/detach-me"]
                )
            self.assertEqual(rc, 0)
            call = runner.run_detached.call_args
            self.assertEqual(call.args[0][0], cli.sys.executable)
            log_path = artifacts / "lsierant_detach-me.log"
            self.assertEqual(call.kwargs["stdout_path"], log_path)
            self.assertEqual(call.kwargs["stderr_path"], log_path)
            # The target worktree must stay untouched: git worktree add
            # refuses a non-empty target.
            self.assertFalse((parent / "lsierant_detach-me").exists())
            out = stderr.getvalue()
            self.assertIn("create detached: pid 4321", out)
            self.assertIn("lsierant_detach-me", out)

    def test_main_strips_detach_from_child_argv(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            parent = Path(td)
            (parent / "anchor").mkdir()
            with (
                mock.patch("wt_ctl.cli.resolve_worktree", return_value=_refs(parent / "anchor")),
                mock.patch("wt_ctl.cli.emit_banner"),
                mock.patch("wt_ctl.cli._spawn_detached_create", return_value=0) as spawn,
            ):
                rc = cli.main(["create", "--detach", "--context", "ctx", "b"])
            self.assertEqual(rc, 0)
            self.assertEqual(spawn.call_args.args[3], ["create", "--context", "ctx", "b"])


if __name__ == "__main__":
    unittest.main()
