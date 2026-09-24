"""``wt-ctl evg spawn`` unit tests.

A FakeRunner records argv and returns scripted ``host list`` payloads across
poll iterations; ``poll_interval_s`` is near zero so the tests don't sleep.
"""

from __future__ import annotations

import json
import os
import unittest
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional
from unittest import mock

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
    handlers: list[tuple[Callable[[list[str]], bool], Callable[[list[str]], CmdResult]]] = field(default_factory=list)

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
                argv=list(argv),
                rc=result.rc,
                stdout=result.stdout,
                stderr=result.stderr,
            )
        return result


def _host_list_result(hosts: list[dict]) -> CmdResult:
    return CmdResult(
        argv=[],
        rc=0,
        stdout=json.dumps(hosts),
        stderr="",
        duration_s=0.0,
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
            _host_list_result(
                [
                    {
                        "id": "i-aaaa1111",
                        "display_name": "alpha",
                        "status": "running",
                        "host_name": "ec2-1-2-3-4.compute.amazonaws.com",
                        "user": "ubuntu",
                    }
                ]
            ),
        )
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="alpha",
            poll_interval_s=0.0,
            timeout_s=2.0,
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
        q = _ListQueue(
            [
                [{"id": "i-bbbb2222", "display_name": "beta", "status": "starting"}],
                [
                    {
                        "id": "i-bbbb2222",
                        "display_name": "beta",
                        "status": "running",
                        "host_name": "ec2-9-8-7-6.compute.amazonaws.com",
                        "user": "ubuntu",
                    }
                ],
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="beta",
            poll_interval_s=0.0,
            timeout_s=2.0,
        )
        self.assertEqual(host_id, "i-bbbb2222")
        joined = [" ".join(c) for c in runner.calls]
        self.assertFalse(any("host create" in j for j in joined))

    # ------------------------------------------------------------------
    def test_fresh_spawn_then_poll_until_running(self) -> None:
        runner = FakeRunner()
        # Resume probe -> empty. Then `host create` returns an id. Then
        # successive `host list` calls walk provisioning -> starting -> running.
        q = _ListQueue(
            [
                [],  # resume detection: no match
                [],  # pre-create snapshot: empty (host appears after)
                [{"id": "i-cccc3333", "display_name": "", "status": "provisioning"}],  # resolve
                [
                    {
                        "id": "i-cccc3333",
                        "display_name": "gamma",
                        "status": "running",
                        "host_name": "ec2-5-5-5-5.compute.amazonaws.com",
                        "user": "ubuntu",
                    }
                ],
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(
            _MatchArgv.host_create,
            _ok(stdout="Host i-cccc3333 spawned with displayName=''.\n"),
        )
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="gamma",
            poll_interval_s=0.0,
            timeout_s=2.0,
            resolve_timeout_s=2.0,
        )
        self.assertEqual(host_id, "i-cccc3333")
        joined = [" ".join(c) for c in runner.calls]
        creates = [j for j in joined if "evergreen host create" in j]
        self.assertEqual(len(creates), 1)
        # And used the env-var key + the requested distro/region defaults.
        self.assertIn("--key lsierant-evg-key", creates[0])
        self.assertIn("--distro ubuntu2204-latest-large", creates[0])
        self.assertIn("--region eu-west-1", creates[0])
        # Tagged at create so a timed-out-but-server-succeeded create still
        # leaves an attributable host (name is reserved -> wt-ctl side label).
        self.assertIn("--tag wt-ctl=gamma", creates[0])

    # ------------------------------------------------------------------
    def test_tag_and_rename_after_aws_id(self) -> None:
        runner = FakeRunner()
        q = _ListQueue(
            [
                [],  # resume probe: empty
                [],  # pre-create snapshot: empty
                [{"id": "i-dddd4444", "display_name": "", "status": "starting"}],  # resolve
                [
                    {
                        "id": "i-dddd4444",
                        "display_name": "delta",
                        "status": "running",
                        "host_name": "ec2-7-7-7-7.compute.amazonaws.com",
                        "user": "ubuntu",
                    }
                ],
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-dddd4444 spawned.\n"))
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        self._domain(runner).spawn(
            name="delta",
            poll_interval_s=0.0,
            timeout_s=2.0,
            resolve_timeout_s=2.0,
        )
        modify_calls = [c for c in runner.calls if c[:3] == ["evergreen", "host", "modify"]]
        self.assertEqual(len(modify_calls), 1, modify_calls)
        argv = modify_calls[0]
        self.assertIn("--host", argv)
        self.assertEqual(argv[argv.index("--host") + 1], "i-dddd4444")
        self.assertIn("--name", argv)
        self.assertEqual(argv[argv.index("--name") + 1], "delta")
        self.assertIn("--tag", argv)
        # Evergreen reserves the ``name`` tag key — using it triggers a
        # 400 from the API, so wt-ctl uses ``wt-ctl=<name>`` as a side
        # label instead.
        self.assertEqual(argv[argv.index("--tag") + 1], "wt-ctl=delta")

    # ------------------------------------------------------------------
    def test_terminal_failure_status_raises(self) -> None:
        runner = FakeRunner()
        q = _ListQueue(
            [
                [],  # resume probe: empty
                [],  # pre-create snapshot: empty
                [{"id": "i-eeee5555", "display_name": "", "status": "failed"}],  # resolve sees it dead
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-eeee5555 spawned.\n"))
        with self.assertRaises(WtCtlError) as ctx:
            self._domain(runner).spawn(
                name="eps",
                poll_interval_s=0.0,
                timeout_s=2.0,
                resolve_timeout_s=2.0,
            )
        self.assertIn("terminal", str(ctx.exception).lower())

    # ------------------------------------------------------------------
    def test_timeout_raises(self) -> None:
        runner = FakeRunner()
        # Always 'starting' — never reaches running. Repeated indefinitely
        # because _ListQueue replays the last entry.
        q = _ListQueue(
            [
                [],  # resume
                [],  # pre-create snapshot
                [{"id": "i-ffff6666", "display_name": "", "status": "starting"}],  # resolve: pick + rename
                [{"id": "i-ffff6666", "display_name": "zeta", "status": "starting"}],  # poll: never running
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-ffff6666 spawned.\n"))
        runner.add(_MatchArgv.host_modify, _ok())
        with self.assertRaises(WtCtlError) as ctx:
            self._domain(runner).spawn(
                name="zeta",
                poll_interval_s=0.0,
                timeout_s=0.05,
                resolve_timeout_s=2.0,
            )
        self.assertIn("timed out", str(ctx.exception).lower())

    # ------------------------------------------------------------------
    def test_create_failure_adopts_server_created_host_without_recreating(self) -> None:
        """A `host create` that fails client-side but made the host server-side
        must be ADOPTED via the pre-create snapshot diff — never re-created
        (which would leak)."""
        runner = FakeRunner()
        q = _ListQueue(
            [
                [],  # resume probe: empty
                [],  # pre-create snapshot: empty
                [{"id": "i-7777aaaa", "display_name": "", "status": "provisioning"}],  # reconcile: adopt
                [{"id": "i-7777aaaa", "display_name": "", "status": "provisioning"}],
                [
                    {
                        "id": "i-7777aaaa",
                        "display_name": "eta",
                        "status": "running",
                        "host_name": "ec2-9-9-9-9.compute.amazonaws.com",
                        "user": "ubuntu",
                    }
                ],
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, CmdResult(argv=[], rc=1, stdout="", stderr="timeout", duration_s=0.0))
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="eta",
            poll_interval_s=0.0,
            timeout_s=2.0,
            reconcile_timeout_s=0.0,
        )
        self.assertEqual(host_id, "i-7777aaaa")
        creates = [c for c in runner.calls if c[:3] == ["evergreen", "host", "create"]]
        self.assertEqual(len(creates), 1, "must adopt, never re-create")

    # ------------------------------------------------------------------
    def test_retry_recreates_only_when_no_host_appeared(self) -> None:
        """When the first create genuinely made no host, a retry re-issues
        `host create`; the second attempt succeeds."""
        runner = FakeRunner()
        q = _ListQueue(
            [
                [],  # resume probe
                [],  # pre-create snapshot
                [],  # reconcile after attempt 1: nothing appeared -> retry
                [{"id": "i-8888bbbb", "display_name": "", "status": "starting"}],
                [
                    {
                        "id": "i-8888bbbb",
                        "display_name": "theta",
                        "status": "running",
                        "host_name": "ec2-8-8-8-8.compute.amazonaws.com",
                        "user": "ubuntu",
                    }
                ],
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        create_calls = {"n": 0}

        def _create(_argv):
            create_calls["n"] += 1
            if create_calls["n"] == 1:
                return CmdResult(argv=[], rc=1, stdout="", stderr="quota", duration_s=0.0)
            return _ok(stdout="Host i-8888bbbb spawned.\n")

        runner.add_fn(_MatchArgv.host_create, _create)
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="theta",
            poll_interval_s=0.0,
            timeout_s=2.0,
            reconcile_timeout_s=0.0,
        )
        self.assertEqual(host_id, "i-8888bbbb")
        self.assertEqual(create_calls["n"], 2, "retry should re-create when nothing appeared")

    # ------------------------------------------------------------------
    def test_placeholder_resolves_via_pre_create_snapshot_diff(self) -> None:
        """When ``host create`` returns only an ``evg-*`` placeholder id and
        a sibling spawn is running concurrently (so the list payload has
        multiple untagged hosts), the poll loop must pick the right host by
        diffing the pre-create snapshot — *not* by display_name (still
        empty) nor by "freshest untagged" (ambiguous).
        """
        runner = FakeRunner()
        # Pre-existing sibling host (already in flight before our create).
        # Note: _RE_AWS_ID requires the id-suffix to be hex (0-9a-f) — so
        # we use ``ab1...`` / ``cd2...`` here, not human-readable labels.
        sibling = {"id": "i-ab1aaaaa", "name": "", "status": "starting", "host_name": "", "user": "ubuntu"}
        # Our new host appears post-create with an unfamiliar id.
        ours_starting = {"id": "i-cd2bbbbb", "name": "", "status": "starting", "host_name": "", "user": "ubuntu"}
        ours_running = {
            "id": "i-cd2bbbbb",
            "name": "wt-x",
            "status": "running",
            "host_name": "ec2-2-2-2-2.compute.amazonaws.com",
            "user": "ubuntu",
        }
        q = _ListQueue(
            [
                [sibling],  # resume probe: no match by name
                [sibling],  # pre-create snapshot: only sibling
                [sibling, ours_starting],  # first poll: ours appears, unnamed
                [sibling, ours_running],  # second poll: ours running, named
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        # create returns the placeholder id (no i-* in stdout).
        runner.add(
            _MatchArgv.host_create,
            _ok(stdout="Host evg-ubuntu2204-9999 spawned.\n"),
        )
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="wt-x",
            poll_interval_s=0.0,
            timeout_s=5.0,
            resolve_timeout_s=2.0,
        )
        # Resolution must pick i-cd2bbbbb (ours), *not* i-ab1aaaaa (the
        # pre-existing sibling, excluded by the pre-create snapshot).
        self.assertEqual(host_id, "i-cd2bbbbb")
        # Sibling id must never appear in any host modify call.
        modify_calls = [c for c in runner.calls if c[:3] == ["evergreen", "host", "modify"]]
        self.assertTrue(modify_calls)
        for c in modify_calls:
            self.assertNotIn("i-ab1aaaaa", c)

    # ------------------------------------------------------------------
    def test_resolve_renames_real_id_ignoring_preexisting_hosts(self) -> None:
        """The spawn lock guarantees at most one NEW nameless host exists
        while we resolve. Resolution renames that host's real ``i-*`` id (not
        the interim ``evg-*`` placeholder), and never touches a pre-existing
        nameless host that was already in the pre-create snapshot.
        """
        runner = FakeRunner()
        # Pre-existing nameless host: present in the snapshot -> excluded.
        preexisting = {"id": "i-ab1aaaaa", "name": "", "status": "starting", "host_name": "", "user": "ubuntu"}
        ours_starting = {"id": "i-cd2bbbbb", "name": "", "status": "starting", "host_name": "", "user": "ubuntu"}
        ours_running = {
            "id": "i-cd2bbbbb",
            "name": "wt-y",
            "status": "running",
            "host_name": "ec2-2-2-2-2.compute.amazonaws.com",
            "user": "ubuntu",
        }
        q = _ListQueue(
            [
                [preexisting],  # resume probe: no match by name
                [preexisting],  # pre-create snapshot -> pre_known = {i-ab1aaaaa}
                [preexisting, ours_starting],  # resolve: only ours is new + nameless
                [preexisting, ours_running],  # poll: ours running
            ]
        )
        runner.add_fn(_MatchArgv.host_list, q)
        runner.add(_MatchArgv.host_create, _ok(stdout="Host evg-ubuntu2204-7777 spawned.\n"))
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        host_id = self._domain(runner).spawn(
            name="wt-y",
            poll_interval_s=0.0,
            timeout_s=5.0,
            resolve_timeout_s=2.0,
        )
        self.assertEqual(host_id, "i-cd2bbbbb")
        # Rename targets the resolved REAL id, not the evg-* placeholder.
        modify_calls = [c for c in runner.calls if c[:3] == ["evergreen", "host", "modify"]]
        self.assertTrue(modify_calls)
        first = modify_calls[0]
        self.assertEqual(first[first.index("--host") + 1], "i-cd2bbbbb")
        self.assertEqual(first[first.index("--name") + 1], "wt-y")
        self.assertEqual(first[first.index("--tag") + 1], "wt-ctl=wt-y")
        # The pre-existing host is never touched.
        for c in modify_calls:
            self.assertNotIn("i-ab1aaaaa", c)


_KEYS_LIST_STDOUT = (
    "Public keys stored in Evergreen:\n"
    "Name: 'lukasz.sierant@mongodb.com', Key: 'ssh-rsa AAAA... lukasz.sierant@mongodb.com'\n"
    "Name: 'evg-host', Key: 'ssh-ed25519 AAAAC3... evg-host'\n"
)


class ResolveKeyNameTests(unittest.TestCase):
    """Cover _resolve_key_name's three resolution branches.

    Order is: MCK_DEVC_EVG_KEY_NAME env var → 'evg-host' if registered →
    first listed key. The upstream ``evergreen keys list`` is plain text
    (no ``--json`` flag), so we parse ``Name: '...'`` lines.
    """

    def setUp(self) -> None:
        self._prev_key = os.environ.pop("MCK_DEVC_EVG_KEY_NAME", None)

    def tearDown(self) -> None:
        if self._prev_key is not None:
            os.environ["MCK_DEVC_EVG_KEY_NAME"] = self._prev_key

    def _domain(self, runner: FakeRunner) -> EvgDomain:
        return EvgDomain(runner=runner, repo_root=Path("/tmp/repo"))

    def test_env_var_takes_precedence(self) -> None:
        os.environ["MCK_DEVC_EVG_KEY_NAME"] = "custom-key"
        runner = FakeRunner()
        # No keys_list handler registered — must not be called.
        self.assertEqual(self._domain(runner)._resolve_key_name(), ("custom-key", ""))
        self.assertFalse(any(c[:3] == ["evergreen", "keys", "list"] for c in runner.calls))

    def test_prefers_evg_host_when_listed(self) -> None:
        runner = FakeRunner()
        runner.add(_MatchArgv.keys_list, _ok(stdout=_KEYS_LIST_STDOUT))
        self.assertEqual(self._domain(runner)._resolve_key_name(), ("evg-host", ""))

    def test_falls_back_to_first_listed_key(self) -> None:
        runner = FakeRunner()
        runner.add(
            _MatchArgv.keys_list,
            _ok(stdout=("Public keys stored in Evergreen:\n" "Name: 'only-one', Key: 'ssh-rsa AAAA only-one'\n")),
        )
        self.assertEqual(self._domain(runner)._resolve_key_name(), ("only-one", ""))

    def test_returns_empty_when_keys_list_fails(self) -> None:
        runner = FakeRunner()
        runner.add(
            _MatchArgv.keys_list,
            CmdResult(argv=[], rc=1, stdout="", stderr="boom", duration_s=0.0),
        )
        name, diag = self._domain(runner)._resolve_key_name()
        self.assertEqual(name, "")
        self.assertIn("rc=1", diag)
        self.assertIn("boom", diag)

    def test_returns_empty_when_no_keys_listed(self) -> None:
        runner = FakeRunner()
        runner.add(
            _MatchArgv.keys_list,
            _ok(stdout="Public keys stored in Evergreen:\n"),
        )
        name, diag = self._domain(runner)._resolve_key_name()
        self.assertEqual(name, "")
        self.assertIn("no parseable", diag)

    def test_spawn_uses_evg_host_when_env_var_unset(self) -> None:
        """End-to-end: with the env var unset, fresh spawn passes
        ``--key evg-host`` to ``evergreen host create``.
        """
        runner = FakeRunner()
        # No live host with this displayName, so resume detection misses.
        runner.add_fn(
            _MatchArgv.host_list,
            _ListQueue(
                [
                    [],
                    [
                        {
                            "id": "i-9999",
                            "display_name": "wt-x",
                            "status": "running",
                            "host_name": "ec2-1-1-1-1.compute.amazonaws.com",
                            "user": "ubuntu",
                        }
                    ],
                ]
            ),
        )
        runner.add(_MatchArgv.keys_list, _ok(stdout=_KEYS_LIST_STDOUT))
        runner.add(_MatchArgv.host_create, _ok(stdout="Host i-9999 spawned.\n"))
        runner.add(_MatchArgv.host_modify, _ok())
        runner.add(_MatchArgv.ssh, _ok())
        self._domain(runner).spawn(
            name="wt-x",
            poll_interval_s=0.0,
            timeout_s=2.0,
        )
        create_calls = [c for c in runner.calls if c[:3] == ["evergreen", "host", "create"]]
        self.assertEqual(len(create_calls), 1)
        self.assertIn("--key", create_calls[0])
        ki = create_calls[0].index("--key")
        self.assertEqual(create_calls[0][ki + 1], "evg-host")


class ListMyHostsJsonTests(unittest.TestCase):
    """`_list_my_hosts_json` must tolerate the Evergreen status banner (prepended
    to stdout) and transient non-zero exits, retrying rather than raising/[]."""

    def _domain(self, runner: FakeRunner) -> EvgDomain:
        return EvgDomain(runner=runner, repo_root=Path("/tmp/repo"))

    def test_parse_strips_leading_banner(self) -> None:
        stdout = 'Banner: We are encountering some slowness with the Github API.\n[{"id": "i-1"}]'
        self.assertEqual(EvgDomain._parse_hosts_json(stdout), [{"id": "i-1"}])

    def test_parse_returns_none_without_array(self) -> None:
        self.assertIsNone(EvgDomain._parse_hosts_json("Banner: totally broken, no json"))

    def test_list_retries_transient_rc_then_succeeds(self) -> None:
        seq = [
            CmdResult(argv=[], rc=1, stdout="", stderr="github slowness", duration_s=0.0),
            CmdResult(argv=[], rc=0, stdout='[{"id": "i-ok"}]', stderr="", duration_s=0.0),
        ]
        runner = FakeRunner()
        it = iter(seq)
        runner.add_fn(_MatchArgv.host_list, lambda _argv: next(it))
        with mock.patch("wt_ctl.domains.evg.time.sleep"):
            out = self._domain(runner)._list_my_hosts_json(retries=2, retry_interval_s=0.0)
        self.assertEqual(out, [{"id": "i-ok"}])

    def test_list_retries_banner_corruption_then_succeeds(self) -> None:
        seq = [
            CmdResult(argv=[], rc=0, stdout="Banner: slowness\nnot-json-at-all", stderr="", duration_s=0.0),
            CmdResult(argv=[], rc=0, stdout='Banner: slowness\n[{"id": "i-ok"}]', stderr="", duration_s=0.0),
        ]
        runner = FakeRunner()
        it = iter(seq)
        runner.add_fn(_MatchArgv.host_list, lambda _argv: next(it))
        with mock.patch("wt_ctl.domains.evg.time.sleep"):
            out = self._domain(runner)._list_my_hosts_json(retries=2, retry_interval_s=0.0)
        self.assertEqual(out, [{"id": "i-ok"}])

    def test_list_raises_after_retries_exhausted(self) -> None:
        runner = FakeRunner()
        runner.add(_MatchArgv.host_list, CmdResult(argv=[], rc=1, stdout="", stderr="down", duration_s=0.0))
        with mock.patch("wt_ctl.domains.evg.time.sleep"):
            with self.assertRaises(ExternalCommandFailed):
                self._domain(runner)._list_my_hosts_json(retries=2, retry_interval_s=0.0)

    def test_list_hosts_retries_transient_rc_then_succeeds(self) -> None:
        """The delete/terminate path (list_hosts) must ride out a transient rc=1
        rather than return [] and silently skip termination -> leak."""
        seq = [
            CmdResult(argv=[], rc=1, stdout="", stderr="github slowness", duration_s=0.0),
            CmdResult(argv=[], rc=0, stdout='[{"id": "i-ok", "name": "wt-x"}]', stderr="", duration_s=0.0),
        ]
        runner = FakeRunner()
        it = iter(seq)
        runner.add_fn(_MatchArgv.host_list, lambda _argv: next(it))
        with mock.patch("wt_ctl.domains.evg.time.sleep"):
            with mock.patch.object(FakeRunner, "have", return_value=True):
                out = self._domain(runner)._host_list_with_retry(mine=True, retries=2, retry_interval_s=0.0)
        self.assertEqual(out, [{"id": "i-ok", "name": "wt-x"}])

    def test_list_hosts_returns_empty_after_exhaustion(self) -> None:
        runner = FakeRunner()
        runner.add(_MatchArgv.host_list, CmdResult(argv=[], rc=1, stdout="", stderr="down", duration_s=0.0))
        with mock.patch("wt_ctl.domains.evg.time.sleep"):
            self.assertEqual(self._domain(runner).list_hosts(mine=True), [])


if __name__ == "__main__":
    unittest.main()
