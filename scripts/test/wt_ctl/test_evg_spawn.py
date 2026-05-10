"""``wt-ctl evg spawn`` unit tests.

The EvgDomain.spawn() method drives the stock ``evergreen`` CLI through a
FakeRunner that records argv and returns scripted ``host list`` payloads
across successive poll iterations. We don't sleep at meaningful intervals —
poll_interval_s is set near zero and tests scripts deliver running on the
expected iteration.
"""

from __future__ import annotations

import json
import os
import unittest
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

from _common import FakePopenFactory  # noqa: E402  (path side-effect only)

from wt_ctl.domains.evg import EvgDomain  # noqa: E402
from wt_ctl.errors import ExternalCommandFailed, WtCtlError  # noqa: E402
from wt_ctl.runner import CmdResult  # noqa: E402


@dataclass
class FakeRunner:
    """Same shape as test_delete.FakeRunner; matchers consult a per-test
    queue of ``host list`` payloads so we can simulate state transitions.
    """

    calls: list[list[str]] = field(default_factory=list)
    handlers: list[tuple[Callable[[list[str]], bool], Callable[[list[str]], CmdResult]]] = field(
        default_factory=list
    )

    def add(self, match, result: CmdResult) -> None:
        self.handlers.append((match, lambda _argv, r=result: r))

    def add_fn(self, match, fn: Callable[[list[str]], CmdResult]) -> None:
        self.handlers.append((match, fn))

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
            raise ExternalCommandFailed(
                argv=list(argv), rc=result.rc,
                stdout=result.stdout, stderr=result.stderr,
            )
        return result


def _host_list_result(hosts: list[dict]) -> CmdResult:
    return CmdResult(
        argv=[], rc=0,
        stdout=json.dumps(hosts), stderr="", duration_s=0.0,
    )


def _ok(stdout: str = "") -> CmdResult:
    return CmdResult(argv=[], rc=0, stdout=stdout, stderr="", duration_s=0.0)


class _ListQueue:
    """Returns scripted ``host list`` payloads in sequence; the last entry
    is replayed indefinitely once the queue is exhausted (saves boilerplate
    in tests that poll past 'running').
    """

    def __init__(self, payloads: list[list[dict]]) -> None:
        self.payloads = payloads
        self.idx = 0

    def __call__(self, _argv) -> CmdResult:
        i = min(self.idx, len(self.payloads) - 1)
        self.idx += 1
        return _host_list_result(self.payloads[i])


class _MatchArgv:
    """Helpers for handler match-clauses."""

    @staticmethod
    def host_list(argv: list[str]) -> bool:
        return argv[:3] == ["evergreen", "host", "list"]

    @staticmethod
    def host_create(argv: list[str]) -> bool:
        return argv[:3] == ["evergreen", "host", "create"]

    @staticmethod
    def host_modify(argv: list[str]) -> bool:
        return argv[:3] == ["evergreen", "host", "modify"]

    @staticmethod
    def keys_list(argv: list[str]) -> bool:
        return argv[:3] == ["evergreen", "keys", "list"]

    @staticmethod
    def ssh(argv: list[str]) -> bool:
        return argv[:1] == ["ssh"]


class EvgSpawnTests(unittest.TestCase):
    def setUp(self) -> None:
        self._prev_key = os.environ.get("MCK_DEVC_EVG_KEY_NAME")
        os.environ["MCK_DEVC_EVG_KEY_NAME"] = "lsierant-evg-key"

    def tearDown(self) -> None:
        if self._prev_key is None:
            os.environ.pop("MCK_DEVC_EVG_KEY_NAME", None)
        else:
            os.environ["MCK_DEVC_EVG_KEY_NAME"] = self._prev_key

    def _domain(self, runner: FakeRunner) -> EvgDomain:
        return EvgDomain(runner=runner, repo_root=Path("/tmp/repo"))

    # ------------------------------------------------------------------
    def test_resume_when_host_exists_running(self) -> None:
        runner = FakeRunner()
        runner.add(
            _MatchArgv.host_list,
            _host_list_result([{
                "id": "i-aaaa1111",
                "display_name": "alpha",
                "status": "running",
                "host_name": "ec2-1-2-3-4.compute.amazonaws.com",
                "user": "ubuntu",
            }]),
        )
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="alpha", poll_interval_s=0.0, timeout_s=2.0,
        )
        self.assertEqual(host_id, "i-aaaa1111")
        joined = [" ".join(c) for c in runner.calls]
        self.assertFalse(any("host create" in j for j in joined))
        # SSH probe used the canonical evg-host key (not ssh-keyscan).
        ssh_calls = [c for c in runner.calls if c[0] == "ssh"]
        self.assertEqual(len(ssh_calls), 1)
        self.assertIn("-i", ssh_calls[0])
        key_idx = ssh_calls[0].index("-i") + 1
        self.assertTrue(ssh_calls[0][key_idx].endswith("/.ssh/evg-host"))

    # ------------------------------------------------------------------
    def test_resume_when_host_exists_starting(self) -> None:
        runner = FakeRunner()
        # Two host_list calls: first the resume-detection probe (starting),
        # then the poll loop (running).
        q = _ListQueue([
            [{"id": "i-bbbb2222", "display_name": "beta",
              "status": "starting"}],
            [{"id": "i-bbbb2222", "display_name": "beta",
              "status": "running",
              "host_name": "ec2-9-8-7-6.compute.amazonaws.com",
              "user": "ubuntu"}],
        ])
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="beta", poll_interval_s=0.0, timeout_s=2.0,
        )
        self.assertEqual(host_id, "i-bbbb2222")
        joined = [" ".join(c) for c in runner.calls]
        self.assertFalse(any("host create" in j for j in joined))

    # ------------------------------------------------------------------
    def test_fresh_spawn_then_poll_until_running(self) -> None:
        runner = FakeRunner()
        # Resume probe -> empty. Then `host create` returns an id. Then
        # successive `host list` calls walk provisioning -> starting -> running.
        q = _ListQueue([
            [],  # resume detection: no match
            [{"id": "i-cccc3333", "display_name": "",
              "status": "provisioning"}],
            [{"id": "i-cccc3333", "display_name": "",
              "status": "starting"}],
            [{"id": "i-cccc3333", "display_name": "gamma",
              "status": "running",
              "host_name": "ec2-5-5-5-5.compute.amazonaws.com",
              "user": "ubuntu"}],
        ])
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(
            _MatchArgv.host_create,
            _ok(stdout="Host i-cccc3333 spawned with displayName=''.\n"),
        )
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="gamma", poll_interval_s=0.0, timeout_s=2.0,
        )
        self.assertEqual(host_id, "i-cccc3333")
        joined = [" ".join(c) for c in runner.calls]
        # `host create` was issued exactly once.
        creates = [j for j in joined if "evergreen host create" in j]
        self.assertEqual(len(creates), 1)
        # And used the env-var key + the requested distro/region defaults.
        self.assertIn("--key lsierant-evg-key", creates[0])
        self.assertIn("--distro ubuntu2204-latest-large", creates[0])
        self.assertIn("--region eu-west-1", creates[0])

    # ------------------------------------------------------------------
    def test_tag_and_rename_after_aws_id(self) -> None:
        runner = FakeRunner()
        q = _ListQueue([
            [],  # resume probe: empty
            [{"id": "i-dddd4444", "display_name": "",
              "status": "starting"}],
            [{"id": "i-dddd4444", "display_name": "delta",
              "status": "running",
              "host_name": "ec2-7-7-7-7.compute.amazonaws.com",
              "user": "ubuntu"}],
        ])
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-dddd4444 spawned.\n"))
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        self._domain(runner).spawn(
            name="delta", poll_interval_s=0.0, timeout_s=2.0,
        )
        modify_calls = [c for c in runner.calls if c[:3] == ["evergreen", "host", "modify"]]
        self.assertEqual(len(modify_calls), 1, modify_calls)
        argv = modify_calls[0]
        self.assertIn("--host", argv)
        self.assertEqual(argv[argv.index("--host") + 1], "i-dddd4444")
        self.assertIn("--name", argv)
        self.assertEqual(argv[argv.index("--name") + 1], "delta")
        self.assertIn("--tag", argv)
        self.assertEqual(argv[argv.index("--tag") + 1], "name=delta")

    # ------------------------------------------------------------------
    def test_terminal_failure_status_raises(self) -> None:
        runner = FakeRunner()
        q = _ListQueue([
            [],  # resume probe: empty
            [{"id": "i-eeee5555", "display_name": "",
              "status": "failed"}],
        ])
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-eeee5555 spawned.\n"))
        with self.assertRaises(WtCtlError) as ctx:
            self._domain(runner).spawn(
                name="eps", poll_interval_s=0.0, timeout_s=2.0,
            )
        self.assertIn("terminal", str(ctx.exception).lower())

    # ------------------------------------------------------------------
    def test_timeout_raises(self) -> None:
        runner = FakeRunner()
        # Always 'starting' — never reaches running. Repeated indefinitely
        # because _ListQueue replays the last entry.
        q = _ListQueue([
            [],
            [{"id": "i-ffff6666", "display_name": "",
              "status": "starting"}],
        ])
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-ffff6666 spawned.\n"))
        runner.add(_MatchArgv.host_modify, _ok())
        with self.assertRaises(WtCtlError) as ctx:
            self._domain(runner).spawn(
                name="zeta", poll_interval_s=0.0, timeout_s=0.05,
            )
        self.assertIn("timed out", str(ctx.exception).lower())


if __name__ == "__main__":
    unittest.main()
