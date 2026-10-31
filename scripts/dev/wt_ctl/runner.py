"""The only module that starts processes. Domain modules take a ``Runner`` and never import ``subprocess``."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from .errors import ExternalCommandFailed, ToolMissing


@dataclass
class CmdResult:
    rc: int
    stdout: str
    stderr: str


class Runner:
    def run(
        self,
        argv: list[str],
        *,
        check: bool = True,
        env: Optional[dict] = None,
        cwd: Optional[Path] = None,
        input_text: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> CmdResult:
        """Run ``argv`` and capture its output. A non-zero exit raises unless ``check`` is false, and so does a
        call that outlives ``timeout`` seconds (exit code 124)."""
        try:
            proc = subprocess.run(
                argv,
                capture_output=True,
                text=True,
                input=input_text,
                env={**os.environ, **env} if env else None,
                cwd=str(cwd) if cwd else None,
                timeout=timeout,
            )
        except FileNotFoundError as exc:
            raise ToolMissing(argv[0], str(exc)) from exc
        except subprocess.TimeoutExpired as exc:
            raise ExternalCommandFailed(argv, 124, f"timed out after {timeout:g}s") from exc
        if check and proc.returncode != 0:
            raise ExternalCommandFailed(argv, proc.returncode, proc.stderr)
        return CmdResult(proc.returncode, proc.stdout, proc.stderr)

    def stream(
        self,
        argv: list[str],
        *,
        log_path: Optional[Path] = None,
        env: Optional[dict] = None,
        cwd: Optional[Path] = None,
    ) -> None:
        """Run ``argv`` with its output on stderr, and appended to ``log_path`` when given."""
        log = None
        if log_path is not None:
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log = log_path.open("a")
        try:
            proc = subprocess.Popen(
                argv,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                env={**os.environ, **env} if env else None,
                cwd=str(cwd) if cwd else None,
            )
        except FileNotFoundError as exc:
            raise ToolMissing(argv[0], str(exc)) from exc
        try:
            for line in proc.stdout:
                sys.stderr.write(line)
                if log is not None:
                    log.write(line)
            proc.wait()
        finally:
            if log is not None:
                log.close()
        if proc.returncode != 0:
            raise ExternalCommandFailed(argv, proc.returncode)

    def exec_replace(self, argv: list[str], *, cwd: Optional[Path] = None, env: Optional[dict] = None) -> None:
        """Replace this process with ``argv``."""
        path = shutil.which(argv[0])
        if path is None:
            raise ToolMissing(argv[0])
        if cwd is not None:
            os.chdir(cwd)
        os.execve(path, argv, {**os.environ, **env} if env else os.environ)

    def have(self, tool: str) -> bool:
        return shutil.which(tool) is not None
