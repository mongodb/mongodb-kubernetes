"""``wt-ctl delete`` orchestrator — each step runs only when its
``delete_*`` flag is True; the orchestrator never deletes git branches."""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from _common import FakePopenFactory  # noqa: E402,F401  (path side-effect only)
from wt_ctl.errors import ExternalCommandFailed  # noqa: E402
from wt_ctl.orchestrator import DeleteInputs, DeleteOrchestrator  # noqa: E402
from wt_ctl.runner import CmdResult  # noqa: E402


@dataclass
class FakeRunner:
    calls: list[list[str]] = field(default_factory=list)
    handlers: list[tuple[Callable[[list[str]], bool], Callable[[list[str]], CmdResult]]] = field(default_factory=list)

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
    if with_evg_host:
        # The `.current-evg-host` pin selects EVG-host mode; without it the
        # --evg dispatch falls through to the local-kind / BYOC branch.
        generated = target / ".generated"
        generated.mkdir(parents=True, exist_ok=True)
        (generated / ".current-evg-host").write_text("topic_x")
    runner = FakeRunner()
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
    return repo, target, runner


class DeletePipelineTests(unittest.TestCase):
    def setUp(self) -> None:
        # Isolate the network-prefix registry so tests never touch ~/.cache/mck-devc/*.
        self._reg_tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._reg_tmp.cleanup)
        self._prev_reg = os.environ.get("MCK_DEVC_REGISTRY_DIR")
        os.environ["MCK_DEVC_REGISTRY_DIR"] = self._reg_tmp.name

    def tearDown(self) -> None:
        if self._prev_reg is None:
            os.environ.pop("MCK_DEVC_REGISTRY_DIR", None)
        else:
            os.environ["MCK_DEVC_REGISTRY_DIR"] = self._prev_reg

    def _seed_registry(self, branch_dir: str, index: int = 24) -> Path:
        reg = Path(self._reg_tmp.name) / "net-prefix-registry"
        reg.parent.mkdir(parents=True, exist_ok=True)
        x = 16 + (index >> 7)
        y_base = (index & 0x7F) << 1
        y_vip = y_base + 1
        port = 8000 + index
        reg.write_text(f"{branch_dir}={index}:{x}:{y_base}:{y_vip}:{port}\n")
        return reg

    def _inputs(self, repo: Path, target: Path, **flags) -> DeleteInputs:
        return DeleteInputs(
            branch="topic/x",
            branch_dir="topic_x",
            worktree_path=target,
            main_repo_root=repo,
            **flags,
        )

    def test_no_targets_is_a_noop(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=True,
                with_evg_host=True,
            )
            self._seed_registry("topic_x")
            DeleteOrchestrator(runner, self._inputs(repo, target)).run()
            self.assertEqual(runner.calls, [])

    def test_all_targets_runs_every_step(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=True,
                with_evg_host=True,
            )
            reg = self._seed_registry("topic_x")
            inputs = self._inputs(
                repo,
                target,
                delete_om=True,
                delete_devc=True,
                delete_evg=True,
                delete_worktree=True,
            )
            DeleteOrchestrator(runner, inputs).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("delete_om_projects.sh" in j for j in joined))
            self.assertTrue(any("docker compose -p topic_x_devcontainer down" in j for j in joined))
            self.assertTrue(any("evergreen host list" in j for j in joined))
            self.assertTrue(any("evergreen host terminate" in j and "i-deadbeef" in j for j in joined))
            self.assertTrue(any("worktree remove --force" in j for j in joined))
            self.assertTrue(any("worktree prune" in j for j in joined))
            self.assertNotIn("topic_x", reg.read_text())
            # Branch is never deleted, regardless of flag combinations.
            self.assertFalse(any("branch -D" in j for j in joined))

    def test_only_evg_terminates_just_the_host(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=True,
                with_evg_host=True,
            )
            reg = self._seed_registry("topic_x")
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_evg=True),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("evergreen host terminate" in j for j in joined))
            self.assertFalse(any("docker compose" in j for j in joined))
            self.assertFalse(any("worktree remove" in j for j in joined))
            self.assertFalse(any("delete_om_projects" in j for j in joined))
            # Net registry untouched when --worktree wasn't selected.
            self.assertIn("topic_x", reg.read_text())

    def test_evg_terminate_recovers_custom_host_name_from_pin(self) -> None:
        # Host created with --evg-host-name; delete invoked without the flag.
        # The pin records the real displayName, so termination must still match.
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=True,
            )
            (target / ".generated" / ".current-evg-host").write_text("my-custom-host")
            runner.handlers.insert(
                0,
                (
                    lambda argv: argv[:3] == ["evergreen", "host", "list"],
                    lambda _argv: CmdResult(
                        argv=list(_argv),
                        rc=0,
                        stdout=_evg_host_json("my-custom-host", "i-custom"),
                        stderr="",
                        duration_s=0.0,
                    ),
                ),
            )
            self._seed_registry("topic_x")
            DeleteOrchestrator(runner, self._inputs(repo, target, delete_evg=True)).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("evergreen host terminate --host i-custom" in j for j in joined))

    def test_only_devc_runs_compose_down_only(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=True,
                with_evg_host=True,
            )
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_devc=True),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("docker compose -p topic_x_devcontainer down" in j for j in joined))
            self.assertFalse(any("evergreen host" in j for j in joined))
            self.assertFalse(any("worktree remove" in j for j in joined))

    def test_worktree_target_releases_prefix(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=False,
            )
            reg = self._seed_registry("topic_x")
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_worktree=True),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("worktree remove --force" in j for j in joined))
            self.assertTrue(any("worktree prune" in j for j in joined))
            self.assertNotIn("topic_x", reg.read_text())

    def test_failed_worktree_removal_keeps_net_prefix(self) -> None:
        # A live worktree must keep its subnet/port; releasing after a failed
        # `git worktree remove` would let another run reclaim an in-use prefix.
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=False,
            )
            runner.handlers.insert(
                0,
                (
                    lambda argv: "worktree" in argv and "remove" in argv,
                    lambda _argv: CmdResult(argv=list(_argv), rc=1, stdout="", stderr="locked", duration_s=0.0),
                ),
            )
            reg = self._seed_registry("topic_x")
            DeleteOrchestrator(runner, self._inputs(repo, target, delete_worktree=True)).run()
            self.assertIn("topic_x", reg.read_text())

    def test_only_om_runs_om_clean(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=False,
            )
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_om=True),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("delete_om_projects.sh" in j for j in joined))
            self.assertFalse(any("docker compose" in j for j in joined))
            self.assertFalse(any("worktree remove" in j for j in joined))

    def test_prefix_release_works_after_worktree_dir_is_gone(self) -> None:
        """Native Registry.release() doesn't depend on any script living
        inside the (already-removed) target worktree.
        """
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=False,
            )
            for f in (target / "scripts" / "dev").glob("*"):
                f.unlink()
            (target / "scripts" / "dev").rmdir()
            reg = self._seed_registry("topic_x")
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_worktree=True),
            ).run()
            self.assertNotIn("topic_x", reg.read_text())

    def test_local_kind_evg_step_deletes_kind_cluster(self) -> None:
        """With no `.current-evg-host`, --evg reads the kubeconfig
        `current-context`; a `kind-` context whose cluster exists is deleted."""
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=False,
            )
            generated = target / ".generated"
            generated.mkdir(parents=True, exist_ok=True)
            (generated / "current.kubeconfig").write_text(
                "apiVersion: v1\nkind: Config\ncurrent-context: kind-lk-smoke\nclusters: []\n"
            )
            runner.handlers.insert(
                0,
                (
                    lambda argv: argv[:3] == ["kind", "get", "clusters"],
                    lambda _argv: CmdResult(
                        argv=list(_argv), rc=0, stdout="lk-smoke\nother-cluster\n", stderr="", duration_s=0.0
                    ),
                ),
            )
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_evg=True),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("kind delete cluster --name lk-smoke" in j for j in joined))
            self.assertFalse(any("evergreen host terminate" in j for j in joined))

    def test_byoc_evg_step_skips_when_context_not_kind(self) -> None:
        """A non-`kind-` current-context is a BYOC cluster we don't own, so
        --evg is a no-op (no kind delete, no evergreen terminate)."""
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=False,
                with_evg_host=False,
            )
            generated = target / ".generated"
            generated.mkdir(parents=True, exist_ok=True)
            (generated / "current.kubeconfig").write_text(
                "apiVersion: v1\nkind: Config\ncurrent-context: gke_my-proj_us-east1-b_prod\n"
            )
            DeleteOrchestrator(
                runner,
                self._inputs(repo, target, delete_evg=True),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertFalse(any("kind delete cluster" in j for j in joined))
            self.assertFalse(any("evergreen host terminate" in j for j in joined))

    def test_failure_in_one_step_does_not_abort_pipeline(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, runner = _make_fixture(
                Path(tmp),
                with_compose_running=True,
                with_evg_host=True,
            )

            def _term(_argv):
                raise ExternalCommandFailed(argv=list(_argv), rc=2, stdout="", stderr="boom")

            runner.handlers.insert(
                0,
                (
                    lambda argv: argv[:3] == ["evergreen", "host", "terminate"],
                    _term,
                ),
            )
            reg = self._seed_registry("topic_x")
            DeleteOrchestrator(
                runner,
                self._inputs(
                    repo,
                    target,
                    delete_om=True,
                    delete_devc=True,
                    delete_evg=True,
                    delete_worktree=True,
                ),
            ).run()
            joined = [" ".join(c) for c in runner.calls]
            self.assertTrue(any("worktree remove" in j for j in joined))
            self.assertNotIn("topic_x", reg.read_text())


if __name__ == "__main__":
    unittest.main()
