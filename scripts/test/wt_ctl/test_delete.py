"""Phase 2: ``wt-ctl delete`` orchestrator.

We drive ``DeleteOrchestrator.run`` with a fake Runner and verify each of
the 6 sub-steps fires by default, and that --keep-* flags suppress
the right step.
"""

from __future__ import annotations

import json
import tempfile
import unittest
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)

from wt_ctl.errors import ExternalCommandFailed  # noqa: E402
from wt_ctl.orchestrator import DeleteInputs, DeleteOrchestrator  # noqa: E402
from wt_ctl.runner import CmdResult  # noqa: E402


@dataclass
class FakeRunner:
    calls: list[list[str]] = field(default_factory=list)
    handlers: list[tuple[Callable[[list[str]], bool], Callable[[list[str]], CmdResult]]] = field(
        default_factory=list
    )

    def add(self, match, result: CmdResult) -> None:
        self.handlers.append((match, lambda _argv, r=result: r))

    def have(self, name: str) -> bool:
        return True

    def _resolve(self, argv):
        for match, handler in self.handlers:
            if match(argv):
                return handler(argv)
        return CmdResult(argv=list(argv), rc=0, stdout="", stderr="", duration_s=0.0)

    def run(self, argv, *, check=True, capture=True, env=None, cwd=None, timeout=None, input_text=None):
        self.calls.append(list(argv))
        result = self._resolve(list(argv))
        if check and result.rc != 0:
            raise ExternalCommandFailed(argv=list(argv), rc=result.rc, stdout=result.stdout, stderr=result.stderr)
        return result

    def run_streaming(self, argv, *, prefix="", log_path=None, env=None, cwd=None):
        self.calls.append(list(argv))
        return self._resolve(list(argv))


def _evg_host_json(name: str, host_id: str, status: str = "running") -> str:
    return json.dumps([{"name": name, "id": host_id, "status": status}])


def _make_fixture(tmp: Path, *, with_compose_running: bool, with_evg_host: bool) -> tuple[Path, Path, FakeRunner]:
    repo = tmp / "main_repo"
    (repo / "scripts" / "dev").mkdir(parents=True)
    target = tmp / "topic_x"
    (target / "scripts" / "dev").mkdir(parents=True)
    (target / ".devcontainer").mkdir()
    (target / ".devcontainer" / ".env").write_text("MCK_DEVC_NET_PREFIX=29\n")
    for script in ("delete_om_projects.sh", "dc_select_network.sh"):
        (target / "scripts" / "dev" / script).write_text("#!/bin/sh\n")
        (target / "scripts" / "dev" / script).chmod(0o755)
        (repo / "scripts" / "dev" / script).write_text("#!/bin/sh\n")
        (repo / "scripts" / "dev" / script).chmod(0o755)
    runner = FakeRunner()
    # docker compose ps tells delete whether a stack is up.
    runner.add(
        lambda argv: argv[:2] == ["docker", "compose"] and "ps" in argv,
        CmdResult(
            argv=[],
            rc=0,
            stdout="topic_x_devcontainer\n" if with_compose_running else "",
            stderr="",
            duration_s=0.0,
        ),
    )
    # evergreen host list --mine --json — used to find host id.
    runner.add(
        lambda argv: argv[:3] == ["evergreen", "host", "list"],
        CmdResult(
            argv=[],
            rc=0,
            stdout=_evg_host_json("topic_x", "i-deadbeef") if with_evg_host else "[]",
            stderr="",
            duration_s=0.0,
        ),
    )
    # git show-ref (used when --delete-branch is set): exit 0 => branch exists.
    runner.add(
        lambda argv: argv[:1] == ["git"] and "show-ref" in argv,
        CmdResult(argv=[], rc=0, stdout="", stderr="", duration_s=0.0),
    )
    return repo, target, runner


class DeletePipelineTests(unittest.TestCase):
    def test_default_runs_five_substeps(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=True, with_evg_host=True,
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            # 1) om clean (delete_om_projects.sh) ran.
            self.assertTrue(any("delete_om_projects.sh" in j for j in joined))
            # 2) compose down ran.
            self.assertTrue(any("docker compose -p topic_x_devcontainer down" in j for j in joined))
            # 3) evergreen host list + terminate ran.
            self.assertTrue(any("evergreen host list" in j for j in joined))
            self.assertTrue(any("evergreen host terminate" in j and "i-deadbeef" in j for j in joined))
            # 4) git worktree remove + prune.
            self.assertTrue(any("worktree remove --force" in j for j in joined))
            self.assertTrue(any("worktree prune" in j for j in joined))
            # 5) network release.
            self.assertTrue(any("dc_select_network.sh --release topic_x" in j for j in joined))
            # 6) NO branch delete (delete_branch=False default).
            self.assertFalse(any("branch -D" in j for j in joined))

    def test_delete_branch_runs_branch_delete(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=False, with_evg_host=False,
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
                delete_branch=True,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("branch -D topic/x" in j for j in joined))

    def test_keep_evg_skips_terminate(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=False, with_evg_host=True,
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
                keep_evg=True,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertFalse(any("evergreen host" in j for j in joined))

    def test_keep_stack_skips_compose_down(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=True, with_evg_host=False,
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
                keep_stack=True,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertFalse(any("docker compose" in j for j in joined))

    def test_keep_worktree_skips_worktree_remove(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=False, with_evg_host=False,
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
                keep_worktree=True,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertFalse(any("worktree remove" in j for j in joined))
            self.assertFalse(any("worktree prune" in j for j in joined))

    def test_keep_om_projects_skips_om_clean(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=False, with_evg_host=False,
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
                keep_om_projects=True,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertFalse(any("delete_om_projects.sh" in j for j in joined))

    def test_prefix_release_falls_back_to_main_repo_when_worktree_gone(self) -> None:
        """After ``_step_worktree_remove`` deletes the target dir the
        in-target script is gone; the release step must fall back to the
        main_repo's checked-out scripts/dev/ — and if that's empty too,
        try cwd as a last resort. We simulate "worktree already removed"
        by deleting the target's scripts/dev/ before run().
        """
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=False, with_evg_host=False,
            )
            # Pretend the worktree has already been removed: delete its
            # scripts/dev/ tree so the in-target candidate fails .is_file().
            for f in (target / "scripts" / "dev").glob("*"):
                f.unlink()
            (target / "scripts" / "dev").rmdir()
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            # Release call still fired, using the main_repo fallback.
            self.assertTrue(
                any("dc_select_network.sh --release topic_x" in j for j in joined),
                msg=f"release didn't fire; calls: {joined}",
            )
            # And the chosen path was the main_repo one (the one that exists).
            release_call = next(
                c for c in runner.calls
                if any("dc_select_network.sh" in a for a in c) and "--release" in c
            )
            self.assertIn(str(repo / "scripts" / "dev" / "dc_select_network.sh"), release_call)

    def test_failure_in_one_step_does_not_abort_pipeline(self) -> None:
        """Best-effort: a failure in evg_terminate must not stop the
        remaining steps (worktree remove, prefix release).
        """
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp), with_compose_running=True, with_evg_host=True,
            )
            # Override evergreen host terminate to fail.
            def _term(_argv):
                raise ExternalCommandFailed(argv=list(_argv), rc=2, stdout="", stderr="boom")
            runner.handlers.insert(
                0,
                (
                    lambda argv: argv[:3] == ["evergreen", "host", "terminate"],
                    _term,
                ),
            )
            inputs = DeleteInputs(
                branch="topic/x", branch_dir="topic_x",
                worktree_path=target, main_repo_root=repo,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            # Subsequent steps still ran.
            self.assertTrue(any("worktree remove" in j for j in joined))
            self.assertTrue(any("dc_select_network.sh --release" in j for j in joined))


if __name__ == "__main__":
    unittest.main()
