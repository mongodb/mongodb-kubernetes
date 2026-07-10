"""KfpDomain — on-host kfp daemon HTTP client (launchctl owns lifecycle).

* ``is_listening()`` — True/False from an injected socket class.
* ``health()`` — injected urlopen: 200/ok parses to "ok"; 503 and
  connection-refused map to None.
* the ``kfp`` CLI parser accepts ``status``/``register`` but not start/stop.
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
from wt_ctl.domains.kfp import KfpDomain  # noqa: E402

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
    """Stand-in Runner that records ``run_detached`` calls."""

    detached_calls: list[dict] = field(default_factory=list)
    spawn_pid: int = 4242

    def run_detached(self, argv, *, stdout_path=None, stderr_path=None, env=None, cwd=None) -> int:
        self.detached_calls.append(
            {
                "argv": list(argv),
                "stdout_path": stdout_path,
                "stderr_path": stderr_path,
            }
        )
        return self.spawn_pid


# ---------------------------------------------------------------------------
# is_listening
# ---------------------------------------------------------------------------


class IsListeningTests(unittest.TestCase):
    def _domain(self, *, connect_ok: bool, tmp: str, exc: Optional[Exception] = None) -> KfpDomain:
        d = KfpDomain(
            runner=None,  # not used by is_listening
            binary=Path("/nonexistent/proxy"),
            state_dir=Path(tmp) / "kfp",
        )

        def _factory(_family, _type):
            return _FakeSocket(connect_ok=connect_ok, exc=exc)

        # Monkey-patch the socket.socket symbol KfpDomain.is_listening uses.
        self._patched_socket = kfp_mod.socket.socket
        kfp_mod.socket.socket = _factory  # type: ignore[assignment]
        return d

    def tearDown(self) -> None:
        if hasattr(self, "_patched_socket"):
            kfp_mod.socket.socket = self._patched_socket

    def test_listening_when_connect_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            d = self._domain(connect_ok=True, tmp=tmp)
            self.assertTrue(d.is_listening())

    def test_not_listening_on_connection_refused(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            d = self._domain(
                connect_ok=False,
                tmp=tmp,
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
# CLI parser
# ---------------------------------------------------------------------------


class KfpCliParserTests(unittest.TestCase):
    def test_kfp_status_parses(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["kfp", "status"])
        self.assertEqual(args.cmd, "kfp")
        self.assertEqual(args.kfp_cmd, "status")

    def test_kfp_register_parses(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["kfp", "register", "--kubeconfig", "/tmp/x"])
        self.assertEqual(args.cmd, "kfp")
        self.assertEqual(args.kfp_cmd, "register")
        self.assertEqual(args.kubeconfig, "/tmp/x")

    def test_kfp_start_rejected(self) -> None:
        parser = cli.build_parser()
        with self.assertRaises(SystemExit):
            parser.parse_args(["kfp", "start"])

    def test_kfp_stop_rejected(self) -> None:
        parser = cli.build_parser()
        with self.assertRaises(SystemExit):
            parser.parse_args(["kfp", "stop"])


if __name__ == "__main__":
    unittest.main()
