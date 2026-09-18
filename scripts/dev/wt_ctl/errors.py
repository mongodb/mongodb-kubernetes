"""Typed exceptions for wt-ctl. ``cli.py`` is the only place that catches
``WtCtlError``; everything else raises and lets the CLI translate.
"""

from __future__ import annotations

from dataclasses import dataclass, field


class WtCtlError(Exception):
    """Base class for typed wt-ctl errors. CLI catches this, prints, exits."""

    # 1 = command failure; subclasses for misuse / preconditions override to 2.
    exit_code: int = 1

    def __init__(self, message: str = ""):
        super().__init__(message)
        self.message = message

    def render(self) -> str:
        return self.message


class ToolMissing(WtCtlError):
    """A required external CLI is not on PATH (docker, devcontainer, ...)."""

    exit_code = 2

    def __init__(self, tool: str, hint: str = ""):
        msg = f"required tool not found on PATH: {tool}"
        if hint:
            msg += f"\n  hint: {hint}"
        super().__init__(msg)
        self.tool = tool


class NotInWorktree(WtCtlError):
    """Cwd is not inside a git worktree of the main repo."""

    exit_code = 2

    def __init__(self, cwd: str, hint: str = ""):
        msg = f"not inside a git worktree of the main repo (cwd={cwd})"
        if hint:
            msg += f"\n  hint: {hint}"
        super().__init__(msg)
        self.cwd = cwd


class BranchNotFound(WtCtlError):
    """Branch was specified but not found locally or on origin."""

    exit_code = 2

    def __init__(self, branch: str):
        super().__init__(f"branch not found: {branch}")
        self.branch = branch


@dataclass
class ExternalCommandFailed(WtCtlError):
    """A wrapped subprocess returned non-zero. Carries the raw command details
    for the CLI / caller to render.
    """

    argv: list[str] = field(default_factory=list)
    rc: int = 0
    stdout: str = ""
    stderr: str = ""

    def __post_init__(self) -> None:
        cmd = " ".join(self.argv) if self.argv else "<no argv>"
        msg = f"command failed (rc={self.rc}): {cmd}"
        if self.stderr.strip():
            tail = "\n".join(self.stderr.strip().splitlines()[-10:])
            msg += f"\n--- stderr ---\n{tail}"
        WtCtlError.__init__(self, msg)


@dataclass
class ParallelPhaseFailures(WtCtlError):
    """Aggregated failures from a parallel phase pair (see
    ``CreateOrchestrator._run_parallel_pair``)."""

    failures: dict[str, ExternalCommandFailed] = field(default_factory=dict)

    def __post_init__(self) -> None:
        names = ", ".join(self.failures.keys())
        msg = f"parallel phase failures: {names}"
        WtCtlError.__init__(self, msg)


class StateConflict(WtCtlError):
    """A precondition / state assertion failed (e.g. devc not running)."""

    exit_code = 2


class RegistryError(WtCtlError):
    """Network registry parse / write error."""

    exit_code = 1


class LockTimeout(WtCtlError):
    """Failed to acquire a lock within the configured timeout."""

    exit_code = 1

    def __init__(self, lock_path: str, timeout_s: float):
        super().__init__(f"could not acquire lock at {lock_path} within {timeout_s:g}s " f"(if stale: rmdir it)")
        self.lock_path = lock_path
        self.timeout_s = timeout_s


class OrphanDetected(WtCtlError):
    """Raised when a destructive op finds an unexpected orphan registration."""

    exit_code = 2
