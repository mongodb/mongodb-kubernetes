"""Errors that ``cli.main`` turns into a one-line message and an exit code."""

from __future__ import annotations


class WtCtlError(Exception):
    exit_code = 1

    def render(self) -> str:
        return str(self)


class ToolMissing(WtCtlError):
    exit_code = 2

    def __init__(self, tool: str, hint: str = ""):
        super().__init__(f"{tool} not found on PATH" + (f" ({hint})" if hint else ""))


class NotInWorktree(WtCtlError):
    exit_code = 2


class WorktreeIndexError(WtCtlError):
    pass


class ExternalCommandFailed(WtCtlError):
    def __init__(self, argv: list[str], rc: int, stderr: str = ""):
        self.argv, self.rc, self.stderr = argv, rc, stderr
        super().__init__(f"command failed (rc={rc}): {' '.join(argv)}")

    def render(self) -> str:
        tail = "\n".join(self.stderr.strip().splitlines()[-5:])
        return f"{self}\n{tail}" if tail else str(self)
