"""Subprocess façade. **Only file in the package permitted to import
``subprocess``** — domain modules receive a ``Runner`` instance.

The lint at ``scripts/test/wt_ctl_lint.py`` enforces this rule.
"""

from __future__ import annotations

import os
import shutil
import subprocess  # noqa: S404 — the single allowed call site (lint excludes runner.py)
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Optional

from .errors import ExternalCommandFailed, ParallelPhaseFailures, ToolMissing


@dataclass
class CmdResult:
    argv: list[str]
    rc: int
    stdout: str
    stderr: str
    duration_s: float


@dataclass
class Job:
    name: str
    argv: list[str]
    log_path: Optional[Path] = None
    cwd: Optional[Path] = None
    env: Optional[dict] = None


class Runner:
    """Wraps subprocess calls.

    Parameters
    ----------
    popen_factory:
        Indirection so unit tests can inject a fake ``subprocess.Popen``-like
        callable without monkey-patching the real module. Defaults to
        ``subprocess.Popen``.
    which:
        Indirection for tool-existence checks; defaults to ``shutil.which``.
    """

    def __init__(
        self,
        popen_factory: Optional[Callable] = None,
        which: Optional[Callable[[str], Optional[str]]] = None,
    ) -> None:
        self._popen = popen_factory or subprocess.Popen
        self._which = which or shutil.which

    # ------------------------------------------------------------------
    # capture-mode (default)
    # ------------------------------------------------------------------
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

    # ------------------------------------------------------------------
    # streaming mode (for build / up / interactive subcommands)
    # ------------------------------------------------------------------
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
            for line in proc.stdout:
                if prefix:
                    sys.stderr.write(f"{prefix}{line}")
                else:
                    sys.stderr.write(line)
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

    # ------------------------------------------------------------------
    # exec-replace (for `wt-ctl attach`)
    # ------------------------------------------------------------------
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
        # Resolve full path so execvpe works predictably.
        path = self._which(argv[0])
        if path is None:
            raise ToolMissing(argv[0])
        os.execvpe(path, argv, merged_env or os.environ.copy())

    # ------------------------------------------------------------------
    # parallel
    # ------------------------------------------------------------------
    def run_parallel(self, jobs: list[Job]) -> dict[str, CmdResult]:
        """Run jobs concurrently. Failures are aggregated and raised together
        as ``ParallelPhaseFailures`` (per plan).
        """
        results: dict[str, CmdResult] = {}
        failures: dict[str, ExternalCommandFailed] = {}
        lock = threading.Lock()

        def _one(job: Job) -> None:
            try:
                if job.log_path is not None:
                    res = self.run_streaming(
                        job.argv,
                        prefix=f"[{job.name}] ",
                        log_path=job.log_path,
                        env=job.env,
                        cwd=job.cwd,
                    )
                else:
                    res = self.run(job.argv, env=job.env, cwd=job.cwd)
                with lock:
                    results[job.name] = res
            except ExternalCommandFailed as failure:
                with lock:
                    failures[job.name] = failure

        if not jobs:
            return results
        with ThreadPoolExecutor(max_workers=max(1, len(jobs))) as pool:
            list(pool.map(_one, jobs))
        if failures:
            raise ParallelPhaseFailures(failures=failures)
        return results

    # ------------------------------------------------------------------
    # helpers
    # ------------------------------------------------------------------
    def have(self, tool: str) -> bool:
        return self._which(tool) is not None

    def _merge_env(self, env: Optional[dict]) -> Optional[dict]:
        if env is None:
            return None
        merged = os.environ.copy()
        merged.update({k: str(v) for k, v in env.items()})
        return merged
