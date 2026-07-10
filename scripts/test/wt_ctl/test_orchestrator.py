"""``wt-ctl create`` orchestrator.

A fake Runner records ``run`` / ``run_streaming`` and lets every call
succeed; the tests assert phase order, state.json transitions, resume /
restart-from behaviour, and concurrency of the parallel pair.
"""

from __future__ import annotations

import os
import tempfile
import threading
import time
import unittest
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

from _common import FakePopenFactory, fake_which  # noqa: E402
from wt_ctl import orchestrator_state as ostate  # noqa: E402
from wt_ctl.errors import ExternalCommandFailed, StateConflict  # noqa: E402
from wt_ctl.orchestrator import CreateInputs, CreateOrchestrator  # noqa: E402
from wt_ctl.runner import CmdResult  # noqa: E402


@dataclass
class FakeRunner:
    """In-test runner that records calls and returns canned results.

    Per-argv handlers are looked up by an "argv signature" function so we
    don't have to enumerate every shell invocation up front.
    """

    calls: list[list[str]] = field(default_factory=list)
    handlers: list[tuple[Callable[[list[str]], bool], Callable[[list[str]], CmdResult]]] = field(default_factory=list)
    parallel_lock: threading.Lock = field(default_factory=threading.Lock)
    concurrent_max: int = 0
    concurrent_now: int = 0

    def add(
        self,
        match: Callable[[list[str]], bool],
        result: CmdResult,
    ) -> None:
        self.handlers.append((match, lambda _argv, r=result: r))

    def add_failure(
        self,
        match: Callable[[list[str]], bool],
        rc: int = 1,
        stderr: str = "boom",
    ) -> None:
        def _fail(argv: list[str]):
            raise ExternalCommandFailed(argv=list(argv), rc=rc, stdout="", stderr=stderr)

        self.handlers.append((match, _fail))

    def have(self, _name: str) -> bool:
        return True

    def _resolve(self, argv: list[str]) -> CmdResult:
        with self.parallel_lock:
            self.concurrent_now += 1
            if self.concurrent_now > self.concurrent_max:
                self.concurrent_max = self.concurrent_now
        try:
            for match, handler in self.handlers:
                if match(argv):
                    return handler(argv)
            return CmdResult(argv=list(argv), rc=0, stdout="", stderr="", duration_s=0.0)
        finally:
            with self.parallel_lock:
                self.concurrent_now -= 1

    def run(self, argv, *, check=True, capture=True, env=None, cwd=None, timeout=None, input_text=None):
        self.calls.append(list(argv))
        result = self._resolve(list(argv))
        if check and result.rc != 0:
            raise ExternalCommandFailed(argv=list(argv), rc=result.rc, stdout=result.stdout, stderr=result.stderr)
        return result

    def run_streaming(self, argv, *, prefix="", log_path=None, env=None, cwd=None):
        self.calls.append(list(argv))
        with self.parallel_lock:
            self.concurrent_now += 1
            if self.concurrent_now > self.concurrent_max:
                self.concurrent_max = self.concurrent_now
        try:
            # Slow down the parallel pair so the other thread can observe
            # concurrent_now=2.
            joined = " ".join(argv)
            if "evg_prepare.sh" in joined or ("devcontainer" in argv and "build" in argv):
                time.sleep(0.05)
            for match, handler in self.handlers:
                if match(argv):
                    return handler(argv)
            return CmdResult(argv=list(argv), rc=0, stdout="", stderr="", duration_s=0.0)
        finally:
            with self.parallel_lock:
                self.concurrent_now -= 1

    def run_parallel(self, jobs):  # not used by the orchestrator directly
        raise NotImplementedError

    def exec_replace(self, argv, *, env=None, cwd=None):
        raise NotImplementedError("not exercised by the orchestrator tests")


def _make_repo_fixture(tmp: Path) -> tuple[Path, Path, str, str]:
    """Build a fake main repo + (target) worktree dir layout that satisfies
    the orchestrator's filesystem assumptions:

      tmp/
        main_repo/
          scripts/dev/{create_worktree.sh,dc_select_network.sh,evg_prepare.sh}
          .devcontainer/scripts/initialize.sh
          .devcontainer/.env  (we let the orchestrator append to this)
          .generated/.current_context
        target/        <- target worktree dir; created by worktree_init.
    """
    repo = tmp / "main_repo"
    (repo / "scripts" / "dev").mkdir(parents=True)
    (repo / ".devcontainer" / "scripts").mkdir(parents=True)
    (repo / ".generated").mkdir(parents=True)
    # Stub scripts so existence checks pass.
    for script in ("create_worktree.sh", "dc_select_network.sh", "evg_prepare.sh", "delete_om_projects.sh"):
        (repo / "scripts" / "dev" / script).write_text("#!/bin/sh\n")
        (repo / "scripts" / "dev" / script).chmod(0o755)
    target = tmp / "target_wt"

    # The target worktree's scripts/dev tree must also exist (orchestrator
    # invokes them inside the target after worktree_init). We pre-create
    # the directory so the orchestrator can write its state.json.
    (target / "scripts" / "dev").mkdir(parents=True)
    for script in ("dc_select_network.sh", "evg_prepare.sh", "delete_om_projects.sh"):
        (target / "scripts" / "dev" / script).write_text("#!/bin/sh\n")
        (target / "scripts" / "dev" / script).chmod(0o755)
    (target / ".devcontainer").mkdir(parents=True)
    (target / ".devcontainer" / "scripts").mkdir(parents=True)
    # initialize.sh is detected via .is_file(); we leave it absent in some
    # tests and present in others.
    return repo, target, "topic/x", "topic_x"


def _make_inputs(repo: Path, target: Path, branch: str, branch_dir: str, **overrides) -> CreateInputs:
    return CreateInputs(
        branch=branch,
        branch_dir=branch_dir,
        worktree_path=target,
        main_repo_root=repo,
        host_worktree_root=repo,
        **overrides,
    )


class CreatePipelineTests(unittest.TestCase):
    def setUp(self) -> None:
        # Isolate the network-prefix registry so net_allocate doesn't
        # touch the real ~/.cache/mck-devc/* during unit tests.
        self._reg_tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._reg_tmp.cleanup)
        self._prev_reg = os.environ.get("MCK_DEVC_REGISTRY_DIR")
        os.environ["MCK_DEVC_REGISTRY_DIR"] = self._reg_tmp.name

    def tearDown(self) -> None:
        if self._prev_reg is None:
            os.environ.pop("MCK_DEVC_REGISTRY_DIR", None)
        else:
            os.environ["MCK_DEVC_REGISTRY_DIR"] = self._prev_reg

    def test_full_pipeline_records_all_phases_ok(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            # initialize.sh present so initialize_hook actually runs.
            (target / ".devcontainer" / "scripts" / "initialize.sh").write_text("#!/bin/sh\n")
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            orch = CreateOrchestrator(runner, inputs)
            orch.run()
            state = ostate.load(target)
            self.assertIsNotNone(state)
            assert state is not None
            for name in ostate.PHASE_ORDER:
                if name == "prepare_e2e":
                    # Opted out via skip_prepare_e2e → recorded SKIPPED (not
                    # left PENDING) so status isn't "incomplete" and --resume
                    # won't re-run it.
                    self.assertEqual(state.phases[name].status, ostate.SKIPPED)
                    continue
                self.assertEqual(
                    state.phases[name].status,
                    ostate.OK,
                    f"phase {name} not ok: {state.phases[name]}",
                )
            # Registry.allocate() writes the 5-line stack-params block:
            # MCK_DEVC_NET_PREFIX plus four derived vars compose.yml reads.
            env_text = (target / ".devcontainer" / ".env").read_text()
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_PREFIX=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_X=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_Y_BASE=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_Y_VIP=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_PROXY_PORT=\d+$")

    def test_evg_prepare_gets_context_explicitly(self) -> None:
        # evg_prepare must receive --context so it doesn't depend on the
        # .generated/.current_context sentinel the concurrent dc_build can clear.
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(
                repo, target, branch, branch_dir, context="e2e_multi_cluster_kind", skip_prepare_e2e=True
            )
            runner = FakeRunner()
            CreateOrchestrator(runner, inputs).run()
            prep = next(c for c in runner.calls if any(a.endswith("evg_prepare.sh") for a in c))
            self.assertIn("--context", prep)
            self.assertEqual(prep[prep.index("--context") + 1], "e2e_multi_cluster_kind")

    def test_phase_order_is_canonical(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            orch = CreateOrchestrator(runner, inputs)
            orch.run()
            # net_allocate issues no subprocess; detect it via the .env prefix
            # line. Order is asserted on the OK-phase sequence in state.json.
            state = ostate.load(target)
            assert state is not None
            for name in (
                "worktree_init",
                "net_allocate",
                "evg_prepare",
                "dc_build",
                "dc_up",
                "post_start",
                "kubeconfig",
            ):
                self.assertEqual(state.phases[name].status, ostate.OK, f"{name} not ok")
            env_text = (target / ".devcontainer" / ".env").read_text()
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_PREFIX=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_X=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_Y_BASE=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_NET_Y_VIP=\d+$")
            self.assertRegex(env_text, r"(?m)^MCK_DEVC_PROXY_PORT=\d+$")
            # Subprocess-detectable phase order still tolerates the
            # parallel pair (evg_prepare, dc_build) in either order.
            sigs = []
            for argv in runner.calls:
                joined = " ".join(argv)
                if "create_worktree.sh" in joined:
                    sigs.append("worktree_init")
                elif "evg_prepare.sh" in joined:
                    sigs.append("evg_prepare")
                elif "devcontainer" in argv and "build" in argv:
                    sigs.append("dc_build")
                elif "devcontainer" in argv and "up" in argv:
                    sigs.append("dc_up")
                elif "post-start.sh" in joined:
                    sigs.append("post_start")
                elif "wt-ctl --quiet kubeconfig refresh" in joined:
                    sigs.append("kubeconfig")
            seen = []
            for s in sigs:
                if s not in seen:
                    seen.append(s)
            allowed = [
                ["worktree_init", "evg_prepare", "dc_build", "dc_up", "post_start", "kubeconfig"],
                ["worktree_init", "dc_build", "evg_prepare", "dc_up", "post_start", "kubeconfig"],
            ]
            self.assertIn(seen, allowed, msg=f"unexpected subprocess order: {seen}")

    def test_parallel_pair_runs_concurrently(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            runner.add(
                lambda argv: argv[0].endswith("dc_select_network.sh") and "--branch-dir" in argv,
                CmdResult(argv=[], rc=0, stdout="MCK_DEVC_NET_PREFIX=21\n", stderr="", duration_s=0.0),
            )
            orch = CreateOrchestrator(runner, inputs)
            orch.run()
            self.assertGreaterEqual(
                runner.concurrent_max,
                2,
                msg=f"expected 2 concurrent runners during parallel pair, got {runner.concurrent_max}",
            )

    def test_failure_marks_phase_failed_and_records_resume_path(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            runner.add(
                lambda argv: argv[0].endswith("dc_select_network.sh") and "--branch-dir" in argv,
                CmdResult(argv=[], rc=0, stdout="MCK_DEVC_NET_PREFIX=22\n", stderr="", duration_s=0.0),
            )
            # Make evg_prepare fail.
            runner.add_failure(
                lambda argv: any(a.endswith("evg_prepare.sh") for a in argv),
                rc=42,
            )
            orch = CreateOrchestrator(runner, inputs)
            with self.assertRaises(Exception):
                orch.run()
            state = ostate.load(target)
            assert state is not None
            self.assertEqual(state.phases["evg_prepare"].status, ostate.FAILED)
            # dc_build runs in parallel with evg_prepare; it should still
            # have completed OK (the FakeRunner is a no-op).
            self.assertEqual(state.phases["dc_build"].status, ostate.OK)
            # Phases after the failed step never started.
            self.assertEqual(state.phases["dc_up"].status, ostate.PENDING)

    def test_unexpected_exception_in_parallel_phase_marks_failed(self) -> None:
        """A parallel phase that raises a non-ExternalCommandFailed exception
        (i.e. a bug — KeyError here) must still be recorded FAILED, not left as
        OK. Otherwise the exception escapes the worker thread, the phase is
        marked OK with a valid input hash, and --resume silently skips the
        crash."""
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()

            def _boom(_argv):
                raise KeyError("unexpected boom in evg_prepare")

            # evg_prepare runs in the parallel pair (via run_streaming).
            runner.handlers.append((lambda argv: any(a.endswith("evg_prepare.sh") for a in argv), _boom))
            orch = CreateOrchestrator(runner, inputs)
            with self.assertRaises(Exception):
                orch.run()
            state = ostate.load(target)
            assert state is not None
            self.assertEqual(
                state.phases["evg_prepare"].status,
                ostate.FAILED,
                msg="unexpected exception must record FAILED, not OK",
            )
            # Later phases must not have started.
            self.assertEqual(state.phases["dc_up"].status, ostate.PENDING)

    def test_net_allocate_repairs_partial_env(self) -> None:
        """A ``.devcontainer/.env`` with only ``MCK_DEVC_NET_PREFIX=<N>`` and
        none of the four derived keys must be repaired on resume, not treated
        as ``done`` — without the derived keys compose.yml's defaults
        (``X=16, Y_BASE=0``) reuse 172.16.0.0/23 across every worktree.
        """
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            # Seed a partial .env (prefix only, no derived keys).
            env_file = target / ".devcontainer" / ".env"
            env_file.parent.mkdir(parents=True, exist_ok=True)
            env_file.write_text("MCK_DEVC_NET_PREFIX=29\n")
            inputs = _make_inputs(
                repo,
                target,
                branch,
                branch_dir,
                skip_prepare_e2e=True,
            )
            runner = FakeRunner()
            CreateOrchestrator(runner, inputs).run()
            text = env_file.read_text()
            # Prefix line preserved exactly — we don't re-allocate when
            # the prefix is already pinned, we only repair the derived
            # keys that were never written.
            self.assertIn("MCK_DEVC_NET_PREFIX=29", text)
            # Derived keys are now present, computed from prefix 29
            # (stack_params(29) → X=16, Y_BASE=58, Y_VIP=59, PORT=8029).
            self.assertIn("MCK_DEVC_NET_X=16", text)
            self.assertIn("MCK_DEVC_NET_Y_BASE=58", text)
            self.assertIn("MCK_DEVC_NET_Y_VIP=59", text)
            self.assertIn("MCK_DEVC_PROXY_PORT=8029", text)
            # No duplicate prefix line.
            self.assertEqual(text.count("MCK_DEVC_NET_PREFIX="), 1)

    def test_resume_skips_ok_phases(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            runner.add(
                lambda argv: argv[0].endswith("dc_select_network.sh") and "--branch-dir" in argv,
                CmdResult(argv=[], rc=0, stdout="MCK_DEVC_NET_PREFIX=23\n", stderr="", duration_s=0.0),
            )
            CreateOrchestrator(runner, inputs).run()
            # Now corrupt state so kubeconfig is failed.
            state = ostate.load(target)
            assert state is not None
            state.set_status("kubeconfig", ostate.FAILED, input_hash=state.phases["kubeconfig"].input_hash)
            ostate.save(target, state)
            # Resume: only kubeconfig should re-run.
            runner2 = FakeRunner()
            runner2.add(
                lambda argv: argv[0].endswith("dc_select_network.sh") and "--branch-dir" in argv,
                CmdResult(argv=[], rc=0, stdout="MCK_DEVC_NET_PREFIX=23\n", stderr="", duration_s=0.0),
            )
            CreateOrchestrator(runner2, inputs).run(resume=True)
            kc_calls = [c for c in runner2.calls if any("wt-ctl --quiet kubeconfig refresh" in a for a in c)]
            self.assertEqual(len(kc_calls), 1)
            # Worktree_init should NOT have re-run.
            wi_calls = [c for c in runner2.calls if any(a.endswith("create_worktree.sh") for a in c)]
            self.assertEqual(len(wi_calls), 0)
            state = ostate.load(target)
            assert state is not None
            self.assertEqual(state.phases["kubeconfig"].status, ostate.OK)

    def test_restart_from_clears_phase_and_later(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            runner.add(
                lambda argv: argv[0].endswith("dc_select_network.sh") and "--branch-dir" in argv,
                CmdResult(argv=[], rc=0, stdout="MCK_DEVC_NET_PREFIX=24\n", stderr="", duration_s=0.0),
            )
            CreateOrchestrator(runner, inputs).run()
            # Restart from reconcile: only reconcile + post_start + kubeconfig should re-run.
            runner2 = FakeRunner()
            runner2.add(
                lambda argv: argv[0].endswith("dc_select_network.sh") and "--branch-dir" in argv,
                CmdResult(argv=[], rc=0, stdout="MCK_DEVC_NET_PREFIX=24\n", stderr="", duration_s=0.0),
            )
            CreateOrchestrator(runner2, inputs).run(restart_from="reconcile")
            # No worktree_init / evg_prepare / dc_build / dc_up should fire.
            for keyword in ("create_worktree.sh", "evg_prepare.sh"):
                cs = [c for c in runner2.calls if any(keyword in a for a in c)]
                self.assertEqual(cs, [], f"phase ran when it shouldn't: {keyword}")
            # kubeconfig DID re-run.
            kc = [c for c in runner2.calls if any("wt-ctl --quiet kubeconfig refresh" in a for a in c)]
            self.assertEqual(len(kc), 1)

    def test_default_run_with_in_progress_state_raises(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            inputs = _make_inputs(repo, target, branch, branch_dir)
            # Pre-write state where evg_prepare is in_progress.
            state = ostate.OrchestratorState.initial(branch=branch, inputs=inputs.to_dict())
            state.set_status("evg_prepare", ostate.IN_PROGRESS, input_hash="x")
            ostate.save(target, state)
            runner = FakeRunner()
            with self.assertRaises(StateConflict):
                CreateOrchestrator(runner, inputs).run()


class JoinKindNetworkTests(unittest.TestCase):
    """The join guard must connect a container whose ``kind`` endpoint is
    absent. ``docker inspect '{{index ...}}'`` prints ``<nil>`` for a missing
    key — only a live endpoint yields ``yes`` under the ``{{if}}`` template."""

    def _orch(self, runner: FakeRunner) -> CreateOrchestrator:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            return CreateOrchestrator(runner, _make_inputs(repo, target, branch, branch_dir))

    def _runner(self, inspect_stdout: str) -> FakeRunner:
        runner = FakeRunner()
        runner.add(
            lambda argv: "compose" in argv and "ps" in argv,
            CmdResult(argv=[], rc=0, stdout="cid123\n", stderr="", duration_s=0.0),
        )
        runner.add(
            lambda argv: "inspect" in argv,
            CmdResult(argv=[], rc=0, stdout=inspect_stdout, stderr="", duration_s=0.0),
        )
        return runner

    def _connects(self, runner: FakeRunner) -> list[list[str]]:
        return [c for c in runner.calls if "network" in c and "connect" in c]

    def test_absent_endpoint_nil_triggers_connect(self) -> None:
        runner = self._runner("<nil>")
        self._orch(runner)._join_kind_network(Path("/wt"), "proj", Path("/tmp/x.log"))
        self.assertEqual(len(self._connects(runner)), 2)  # devcontainer + k8s-proxy

    def test_live_endpoint_skips_connect(self) -> None:
        runner = self._runner("yes")
        self._orch(runner)._join_kind_network(Path("/wt"), "proj", Path("/tmp/x.log"))
        self.assertEqual(self._connects(runner), [])


class InPlaceTests(unittest.TestCase):
    """--in-place skips create_worktree.sh and operates in the checkout."""

    def test_in_place_skips_create_worktree(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            # worktree_path == main repo, as cmd_create sets for --in-place.
            inputs = _make_inputs(repo, repo, branch, branch_dir, in_place=True, skip_prepare_e2e=True)
            runner = FakeRunner()
            CreateOrchestrator(runner, inputs).run()
            created = [c for c in runner.calls if any("create_worktree.sh" in a for a in c)]
            self.assertEqual(created, [], "create_worktree.sh must not run with --in-place")


class WithPersistedFlagsTests(unittest.TestCase):
    """--resume must reload behavioural flags from state.json, not re-default
    an omitted CLI flag (which flips phase branches / invalidates hashes)."""

    def test_omitted_local_kind_reloads_from_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            original = _make_inputs(repo, target, branch, branch_dir, local_kind=True, multi_cluster=True)
            saved = original.to_dict()
            # Simulate a bare `--resume`: CLI re-defaults local_kind to False.
            resumed = _make_inputs(repo, target, branch, branch_dir, local_kind=False, multi_cluster=True)
            merged = resumed.with_persisted_flags(saved)
            self.assertTrue(merged.local_kind)
            self.assertTrue(merged.multi_cluster)
            # Paths stay freshly resolved (not clobbered by persisted strings).
            self.assertEqual(merged.worktree_path, resumed.worktree_path)

    def test_single_cluster_run_not_flipped_to_multi_on_resume(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            saved = _make_inputs(repo, target, branch, branch_dir, multi_cluster=False).to_dict()
            # Bare resume: CLI default is multi_cluster=True.
            resumed = _make_inputs(repo, target, branch, branch_dir, multi_cluster=True)
            self.assertFalse(resumed.with_persisted_flags(saved).multi_cluster)


class ReconcileTests(unittest.TestCase):
    """reconcile force-recreates sidecar services from compose.user.yml but
    never the main `devcontainer` service (the devcontainer CLI owns it;
    recreating it out-of-band breaks subsequent `devcontainer exec`)."""

    def _recreated_services(self, calls: list[list[str]]) -> list[str]:
        out = []
        for c in calls:
            if "--force-recreate" in c and "up" in c:
                out.append(c[-1])  # svc is the trailing arg
        return out

    def test_reconcile_skips_devcontainer_service(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo, target, branch, branch_dir = _make_repo_fixture(Path(tmp))
            (target / ".devcontainer" / "compose.user.yml").write_text(
                "services:\n"
                "  devcontainer:\n"
                "    environment:\n"
                '      EVR_TASK_ID: "abc"\n'
                "  k8s-proxy:\n"
                "    environment:\n"
                "      FOO: bar\n"
            )
            inputs = _make_inputs(repo, target, branch, branch_dir, skip_prepare_e2e=True)
            runner = FakeRunner()
            CreateOrchestrator(runner, inputs).run()
            recreated = self._recreated_services(runner.calls)
            self.assertIn("k8s-proxy", recreated)
            self.assertNotIn("devcontainer", recreated)


if __name__ == "__main__":
    unittest.main()
