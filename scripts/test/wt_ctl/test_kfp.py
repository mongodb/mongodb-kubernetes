"""KfpDomain — on-host kfp daemon lifecycle.

We exercise:

* ``is_listening()`` — returns True/False based on a socket-class injected
  via test seam.
* ``health()`` — uses an injected urlopen to assert 200/ok parsing,
  503 / connection refused mapping to None.
* ``start()`` — invokes ``Runner.run_detached`` with the expected argv
  and writes a pidfile when the daemon isn't already up; no-ops when up.
* the ``cmd_kfp`` CLI entry point accepts ``status`` / ``start`` / ``stop``
  via the argparse parser.
"""

from __future__ import annotations

import io
import os
import tempfile
import unittest
import urllib.error
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

from _common import fake_which  # noqa: E402

from wt_ctl import cli  # noqa: E402
from wt_ctl.domains import kfp as kfp_mod  # noqa: E402
from wt_ctl.domains.kfp import KfpDomain, KfpStartFailed  # noqa: E402


# ---------------------------------------------------------------------------
# fake socket / urlopen helpers
# ---------------------------------------------------------------------------

class _FakeSocket:
    """Minimal stand-in for ``socket.socket(AF_INET, SOCK_STREAM)``."""

    def __init__(self, *, connect_ok: bool, exc: Optional[Exception] = None) -> None:
        self._connect_ok = connect_ok
        self._exc = exc
        self.timeout: Optional[float] = None
        self.closed = False

    def settimeout(self, t: float) -> None:
        self.timeout = t

    def connect(self, _addr) -> None:
        if not self._connect_ok:
            raise self._exc or ConnectionRefusedError("refused")

    def close(self) -> None:
        self.closed = True


class _FakeHTTPResponse:
    def __init__(self, status: int, body: bytes) -> None:
        self.status = status
        self._body = body

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc) -> None:
        return None


@dataclass
class _DetachedRecorder:
    """Stand-in Runner used for the ``start()`` test."""
    detached_calls: list[dict] = field(default_factory=list)
    spawn_pid: int = 4242

    def run_detached(self, argv, *, stdout_path=None, stderr_path=None,
                     env=None, cwd=None) -> int:
        self.detached_calls.append({
            "argv": list(argv),
            "stdout_path": stdout_path,
            "stderr_path": stderr_path,
        })
        return self.spawn_pid


# ---------------------------------------------------------------------------
# is_listening
# ---------------------------------------------------------------------------

class IsListeningTests(unittest.TestCase):
    def _domain(self, *, connect_ok: bool, tmp: str,
                exc: Optional[Exception] = None) -> KfpDomain:
        d = KfpDomain(
            runner=None,  # not used by is_listening
            binary=Path("/nonexistent/proxy"),
            state_dir=Path(tmp) / "kfp",
        )

        def _factory(_family, _type):
            return _FakeSocket(connect_ok=connect_ok, exc=exc)

        # Inject the fake socket factory by monkey-patching the module-level
        # socket.socket symbol used by KfpDomain.is_listening.
        self._patched_socket = kfp_mod.socket.socket
        kfp_mod.socket.socket = _factory  # type: ignore[assignment]
        return d

    def tearDown(self) -> None:
        # Always restore.
        if hasattr(self, "_patched_socket"):
            kfp_mod.socket.socket = self._patched_socket

    def test_listening_when_connect_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            d = self._domain(connect_ok=True, tmp=tmp)
            self.assertTrue(d.is_listening())

    def test_not_listening_on_connection_refused(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            d = self._domain(
                connect_ok=False, tmp=tmp,
                exc=ConnectionRefusedError("refused"),
            )
            self.assertFalse(d.is_listening())

    def test_not_listening_on_timeout(self) -> None:
        import socket as _socket
        with tempfile.TemporaryDirectory() as tmp:
            d = self._domain(connect_ok=False, tmp=tmp, exc=_socket.timeout())
            self.assertFalse(d.is_listening())


# ---------------------------------------------------------------------------
# health()
# ---------------------------------------------------------------------------

class HealthTests(unittest.TestCase):
    def setUp(self) -> None:
        self._orig_urlopen = kfp_mod.urllib.request.urlopen

    def tearDown(self) -> None:
        kfp_mod.urllib.request.urlopen = self._orig_urlopen

    def _patch(self, fn: Callable) -> None:
        kfp_mod.urllib.request.urlopen = fn  # type: ignore[assignment]

    def _domain(self, tmp: str) -> KfpDomain:
        return KfpDomain(
            runner=None,
            binary=Path("/nonexistent/proxy"),
            state_dir=Path(tmp) / "kfp",
        )

    def test_200_ok_returns_ok(self) -> None:
        self._patch(lambda _u, timeout=None: _FakeHTTPResponse(200, b"ok"))
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(self._domain(tmp).health(), "ok")

    def test_503_returns_none(self) -> None:
        self._patch(lambda _u, timeout=None: _FakeHTTPResponse(503, b"down"))
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(self._domain(tmp).health())

    def test_connection_refused_returns_none(self) -> None:
        def _boom(_u, timeout=None):
            raise urllib.error.URLError("Connection refused")
        self._patch(_boom)
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(self._domain(tmp).health())

    def test_oserror_returns_none(self) -> None:
        def _boom(_u, timeout=None):
            raise OSError("boom")
        self._patch(_boom)
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(self._domain(tmp).health())


# ---------------------------------------------------------------------------
# start()
# ---------------------------------------------------------------------------

class StartTests(unittest.TestCase):
    def setUp(self) -> None:
        self._orig_socket = kfp_mod.socket.socket
        self._orig_urlopen = kfp_mod.urllib.request.urlopen

    def tearDown(self) -> None:
        kfp_mod.socket.socket = self._orig_socket
        kfp_mod.urllib.request.urlopen = self._orig_urlopen

    def _set_listening(self, value: bool) -> None:
        def _factory(_f, _t):
            return _FakeSocket(connect_ok=value)
        kfp_mod.socket.socket = _factory  # type: ignore[assignment]

    def _set_health(self, body: bytes, status: int = 200) -> None:
        def _ok(_u, timeout=None):
            return _FakeHTTPResponse(status, body)
        kfp_mod.urllib.request.urlopen = _ok  # type: ignore[assignment]

    def test_no_op_when_already_listening(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self._set_listening(True)
            self._set_health(b"ok")
            runner = _DetachedRecorder()
            binary = Path(tmp) / "proxy"
            binary.write_text("#!/bin/sh\nexit 0\n")
            binary.chmod(0o755)
            d = KfpDomain(runner=runner, binary=binary,
                          state_dir=Path(tmp) / "kfp")
            d.start()
            self.assertEqual(runner.detached_calls, [])

    def test_invokes_runner_with_expected_argv_when_not_up(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self._set_listening(False)
            self._set_health(b"ok")
            runner = _DetachedRecorder()
            binary = Path(tmp) / "proxy"
            binary.write_text("#!/bin/sh\nexit 0\n")
            binary.chmod(0o755)
            state_dir = Path(tmp) / "kfp"
            d = KfpDomain(runner=runner, binary=binary, state_dir=state_dir)
            pid = d.start()
            self.assertEqual(pid, runner.spawn_pid)
            self.assertEqual(len(runner.detached_calls), 1)
            call = runner.detached_calls[0]
            self.assertEqual(call["argv"], [str(binary)])
            self.assertEqual(call["stdout_path"], state_dir / "log")
            self.assertEqual(call["stderr_path"], state_dir / "log")
            # Pidfile recorded.
            self.assertTrue((state_dir / "pid").is_file())
            self.assertEqual(
                (state_dir / "pid").read_text().strip(),
                str(runner.spawn_pid),
            )

    def test_missing_binary_raises(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self._set_listening(False)
            runner = _DetachedRecorder()
            d = KfpDomain(
                runner=runner,
                binary=Path(tmp) / "no-such-proxy",
                state_dir=Path(tmp) / "kfp",
            )
            with self.assertRaises(KfpStartFailed):
                d.start()


# ---------------------------------------------------------------------------
# CLI parser
# ---------------------------------------------------------------------------

class KfpCliParserTests(unittest.TestCase):
    def test_kfp_status_parses(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["kfp", "status"])
        self.assertEqual(args.cmd, "kfp")
        self.assertEqual(args.kfp_cmd, "status")

    def test_kfp_start_parses(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["kfp", "start"])
        self.assertEqual(args.cmd, "kfp")
        self.assertEqual(args.kfp_cmd, "start")

    def test_kfp_stop_parses(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["kfp", "stop"])
        self.assertEqual(args.cmd, "kfp")
        self.assertEqual(args.kfp_cmd, "stop")


if __name__ == "__main__":
    unittest.main()
