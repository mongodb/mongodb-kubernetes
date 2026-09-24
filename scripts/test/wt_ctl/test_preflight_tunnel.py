"""Unit tests for ``CreateOrchestrator._preflight_evg_tunnel``.

The preflight runs an ``ssh`` probe inside the ``evg-host-proxy`` compose
service to prove the devc -> EVG host path (forwarded ssh-agent + autossh
target) before the destructive prepare and the apiserver probe.
"""

from __future__ import annotations

import types
import unittest
from dataclasses import dataclass, field
from pathlib import Path
from unittest import mock

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)
from wt_ctl.errors import WtCtlError  # noqa: E402
from wt_ctl.orchestrator import CreateOrchestrator  # noqa: E402
from wt_ctl.runner import CmdResult  # noqa: E402

WT = Path("/tmp/wt-preflight-tunnel")


@dataclass
class FakeRunner:
    compose_ps_stdout: str = "cid-123\n"
    exec_results: list[int] = field(default_factory=list)
    logs_stdout: str = ""
    logs_stderr: str = ""
    calls: list[list[str]] = field(default_factory=list)

    def run(self, argv, *, check=True, capture=True, env=None, cwd=None, timeout=None, input_text=None):
        self.calls.append(list(argv))
        if argv[:2] == ["docker", "compose"]:
            return CmdResult(argv=list(argv), rc=0, stdout=self.compose_ps_stdout, stderr="", duration_s=0.0)
        if argv[:2] == ["docker", "exec"]:
            rc = self.exec_results.pop(0) if self.exec_results else 0
            return CmdResult(argv=list(argv), rc=rc, stdout="", stderr="probe failed", duration_s=0.0)
        if argv[:2] == ["docker", "logs"]:
            return CmdResult(argv=list(argv), rc=0, stdout=self.logs_stdout, stderr=self.logs_stderr, duration_s=0.0)
        return CmdResult(argv=list(argv), rc=0, stdout="", stderr="", duration_s=0.0)


def _orchestrator(runner: FakeRunner, *, local_kind: bool = False):
    return types.SimpleNamespace(runner=runner, inputs=types.SimpleNamespace(local_kind=local_kind))


class PreflightEvgTunnelTests(unittest.TestCase):
    def test_success_on_first_attempt(self) -> None:
        runner = FakeRunner(exec_results=[0])
        with mock.patch("wt_ctl.orchestrator.time.sleep"):
            CreateOrchestrator._preflight_evg_tunnel(_orchestrator(runner), WT, WT / "logs")
        self.assertEqual(sum(1 for c in runner.calls if c[:2] == ["docker", "exec"]), 1)

    def test_local_kind_skips_entirely(self) -> None:
        runner = FakeRunner()
        CreateOrchestrator._preflight_evg_tunnel(_orchestrator(runner, local_kind=True), WT, WT / "logs")
        self.assertEqual(runner.calls, [])

    def test_missing_proxy_container_raises(self) -> None:
        runner = FakeRunner(compose_ps_stdout="")
        with self.assertRaises(WtCtlError) as ctx:
            CreateOrchestrator._preflight_evg_tunnel(_orchestrator(runner), WT, WT / "logs")
        self.assertIn("evg-host-proxy container is not running", str(ctx.exception))

    def test_repeated_failures_raise_with_log_tail_and_hint(self) -> None:
        runner = FakeRunner(
            exec_results=[1] * 15,
            logs_stderr="autossh: ubuntu@ec2-host: Permission denied (publickey).\n",
        )
        with mock.patch("wt_ctl.orchestrator.time.sleep"):
            with self.assertRaises(WtCtlError) as ctx:
                CreateOrchestrator._preflight_evg_tunnel(_orchestrator(runner), WT, WT / "logs")
        message = str(ctx.exception)
        self.assertIn("Permission denied (publickey)", message)
        self.assertIn("ssh-add ~/.ssh/evg-host", message)
        self.assertEqual(sum(1 for c in runner.calls if c[:2] == ["docker", "exec"]), 15)


if __name__ == "__main__":
    unittest.main()
