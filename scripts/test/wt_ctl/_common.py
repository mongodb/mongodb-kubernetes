"""Shared test scaffolding: fake Popen + helpers to load wt_ctl from source."""

from __future__ import annotations

import io
import os
import sys
from pathlib import Path
from typing import Optional

# Make `import wt_ctl` work when this test is run from any cwd.
_REPO = Path(__file__).resolve().parents[3]
_PKG_PARENT = _REPO / "scripts" / "dev"
if str(_PKG_PARENT) not in sys.path:
    sys.path.insert(0, str(_PKG_PARENT))


class FakeProc:
    """Mimics subprocess.Popen sufficiently for Runner.run + run_streaming."""

    def __init__(self, stdout: str = "", stderr: str = "", rc: int = 0) -> None:
        self._stdout = io.StringIO(stdout)
        self._stderr_text = stderr
        self.returncode: Optional[int] = rc
        self.stdin: Optional[io.StringIO] = None

    @property
    def stdout(self) -> io.StringIO:
        return self._stdout

    @property
    def stderr(self) -> io.StringIO:
        return io.StringIO(self._stderr_text)

    def communicate(self, input: Optional[str] = None, timeout: Optional[float] = None):
        return self._stdout.getvalue(), self._stderr_text

    def wait(self, timeout: Optional[float] = None) -> int:
        return self.returncode or 0

    def kill(self) -> None:
        pass


class FakePopenFactory:
    """Pluggable popen-factory: maps argv tuples (or first argv element) to
    canned (stdout, stderr, rc) tuples. Falls back to `default`.
    """

    def __init__(self, mapping: dict[tuple, tuple[str, str, int]], default: tuple[str, str, int] = ("", "", 0)) -> None:
        self.mapping = mapping
        self.default = default
        self.calls: list[list[str]] = []

    def __call__(self, argv, **kwargs) -> FakeProc:
        self.calls.append(list(argv))
        key = tuple(argv)
        if key in self.mapping:
            stdout, stderr, rc = self.mapping[key]
            return FakeProc(stdout, stderr, rc)
        # try matching by leading prefix (handy for git -C variations)
        for k, v in self.mapping.items():
            if tuple(argv[: len(k)]) == k:
                return FakeProc(*v)
        return FakeProc(*self.default)


def fake_which(_name: str) -> str:
    """Always-present tool: tests don't actually exec anything."""
    return f"/usr/bin/{_name}"
