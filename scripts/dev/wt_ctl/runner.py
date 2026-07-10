"""Subprocess façade. **Only file in the package permitted to import
``subprocess``** — domain modules receive a ``Runner`` instance.

The lint at ``scripts/test/wt_ctl_lint.py`` enforces this rule.
"""

from __future__ import annotations

import os
import shutil
import subprocess  # noqa: S404 — the single allowed call site (lint excludes runner.py)
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Optional

from .errors import ExternalCommandFailed, ToolMissing


@dataclass
class CmdResult:
    argv: list[str]
    rc: int
    stdout: str
    stderr: str
    duration_s: float


class Runner:
    """Wraps subprocess calls.

    ``popen_factory`` / ``which`` are injectable so unit tests can supply fakes
    without monkey-patching; they default to ``subprocess.Popen`` / ``shutil.which``.
    """

    def __init__(
        self,
        popen_factory: Optional[Callable] = None,
        which: Optional[Callable[[str], Optional[str]]] = None,
    ) -> None:
        self._popen = popen_factory or subprocess.Popen
        self._which = which or shutil.which

    def run(
        self,
        argv: list[str],
        *,
        check: bool = True,
        capture: bool = True,
        env: Optional[dict] = None,
        cwd: Optional[Path] = None,
        timeout: Optional[float] = None,
        input_text: Optional[str] = None,
    ) -> CmdResult:
        if not argv:
            raise ValueError("argv must be non-empty")
        merged_env = self._merge_env(env)
        start = time.monotonic()
        if capture:
            stdout_arg = subprocess.PIPE
            stderr_arg = subprocess.PIPE
        else:
            stdout_arg = None
            stderr_arg = None
        try:
            proc = self._popen(
                argv,
                stdin=subprocess.PIPE if input_text is not None else None,
                stdout=stdout_arg,
                stderr=stderr_arg,
                env=merged_env,
                cwd=str(cwd) if cwd else None,
                text=True,
            )
        except FileNotFoundError as exc:
            raise ToolMissing(argv[0], hint=str(exc)) from exc
        try:
            stdout, stderr = proc.communicate(input=input_text, timeout=timeout)
        except subprocess.TimeoutExpired as exc:
            proc.kill()
            stdout, stderr = proc.communicate()
            raise ExternalCommandFailed(
                argv=list(argv),
                rc=proc.returncode if proc.returncode is not None else -1,
                stdout=stdout or "",
                stderr=(stderr or "") + f"\n[wt-ctl] timeout after {timeout}s",
            ) from exc
        duration = time.monotonic() - start
        rc = proc.returncode
        result = CmdResult(
            argv=list(argv),
            rc=rc,
            stdout=stdout or "",
            stderr=stderr or "",
            duration_s=duration,
        )
        if check and rc != 0:
            raise ExternalCommandFailed(
                argv=result.argv,
                rc=rc,
                stdout=result.stdout,
                stderr=result.stderr,
            )
        return result

    def run_streaming(
        self,
        argv: list[str],
        *,
        prefix: str = "",
        log_path: Optional[Path] = None,
        env: Optional[dict] = None,
        cwd: Optional[Path] = None,
    ) -> CmdResult:
        """Forward stdout+stderr to the parent's stderr (live), optionally
        tee'd to ``log_path``. Returns CmdResult with empty stdout/stderr.
        """
        merged_env = self._merge_env(env)
        if log_path is not None:
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log_fh = log_path.open("a")
        else:
            log_fh = None
        start = time.monotonic()
        try:
            try:
                proc = self._popen(
                    argv,
                    stdin=None,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT,
                    env=merged_env,
                    cwd=str(cwd) if cwd else None,
                    text=True,
                    bufsize=1,
                )
            except FileNotFoundError as exc:
                raise ToolMissing(argv[0], hint=str(exc)) from exc
            assert proc.stdout is not None
            tty = sys.stderr.isatty()
            for line in proc.stdout:
                out = f"{prefix}{line}" if prefix else line
                # Tools reading a PTY in raw mode emit bare \n; on a TTY without
                # ONLCR that staircases. Normalize to CRLF on TTY only — logs stay LF.
                if tty:
                    out = out.replace("\r\n", "\n").replace("\n", "\r\n")
                sys.stderr.write(out)
                sys.stderr.flush()
                if log_fh is not None:
                    log_fh.write(line)
                    log_fh.flush()
            proc.wait()
        finally:
            if log_fh is not None:
                log_fh.close()
        rc = proc.returncode
        duration = time.monotonic() - start
        result = CmdResult(argv=list(argv), rc=rc, stdout="", stderr="", duration_s=duration)
        if rc != 0:
            raise ExternalCommandFailed(argv=result.argv, rc=rc, stdout="", stderr="")
        return result

    def run_detached(
        self,
        argv: list[str],
        *,
        stdout_path: Optional[Path] = None,
        stderr_path: Optional[Path] = None,
        env: Optional[dict] = None,
        cwd: Optional[Path] = None,
    ) -> int:
        """Start ``argv`` in the background, fully detached from this process.

        New session so it survives the orchestrator exiting; stdin from
        /dev/null; stdout/stderr appended to the given paths (opened once when
        both point at the same path). Returns the PID immediately without
        waiting for the child to come up — callers should health-check.
        """
        if not argv:
            raise ValueError("argv must be non-empty")
        merged_env = self._merge_env(env)
        out_fh = None
        err_fh = None
        try:
            stdin_fh = open(os.devnull, "rb")
            if stdout_path is not None:
                stdout_path.parent.mkdir(parents=True, exist_ok=True)
                out_fh = open(stdout_path, "ab")
            if stderr_path is not None:
                if stderr_path == stdout_path and out_fh is not None:
                    err_fh = out_fh
                else:
                    stderr_path.parent.mkdir(parents=True, exist_ok=True)
                    err_fh = open(stderr_path, "ab")
            try:
                proc = self._popen(
                    argv,
                    stdin=stdin_fh,
                    stdout=out_fh if out_fh is not None else subprocess.DEVNULL,
                    stderr=err_fh if err_fh is not None else subprocess.DEVNULL,
                    env=merged_env,
                    cwd=str(cwd) if cwd else None,
                    start_new_session=True,
                    close_fds=True,
                )
            except FileNotFoundError as exc:
                raise ToolMissing(argv[0], hint=str(exc)) from exc
            return proc.pid
        finally:
            # Close the parent's FD copies; the child has its own from Popen.
            try:
                stdin_fh.close()
            except (OSError, NameError):
                pass
            try:
                if out_fh is not None:
                    out_fh.close()
            except OSError:
                pass
            if err_fh is not None and err_fh is not out_fh:
                try:
                    err_fh.close()
                except OSError:
                    pass

    def exec_replace(
        self,
        argv: list[str],
        *,
        env: Optional[dict] = None,
        cwd: Optional[Path] = None,
    ) -> None:
        """Replace the current process with ``argv`` — never returns."""
        if not argv:
            raise ValueError("argv must be non-empty")
        merged_env = self._merge_env(env)
        if cwd is not None:
            os.chdir(str(cwd))
        path = self._which(argv[0])
        if path is None:
            raise ToolMissing(argv[0])
        os.execvpe(path, argv, merged_env or os.environ.copy())

    def have(self, tool: str) -> bool:
        return self._which(tool) is not None

    def _merge_env(self, env: Optional[dict]) -> Optional[dict]:
        if env is None:
            return None
        merged = os.environ.copy()
        merged.update({k: str(v) for k, v in env.items()})
        return merged
