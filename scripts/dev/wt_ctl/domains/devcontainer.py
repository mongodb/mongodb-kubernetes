"""Wraps the ``devcontainer`` CLI — up / build / exec / attach."""

from __future__ import annotations

import os
from pathlib import Path
from typing import Optional

from ..errors import ToolMissing
from ..paths import logs_dir
from ..runner import Runner

_TMUX_LOAD_CMD = "exec tmuxp load -y /workspace/.devcontainer/tmuxp/mck.yaml"


class DevcontainerDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    # ------------------------------------------------------------------
    # build / up
    # ------------------------------------------------------------------
    def build(self, worktree_root: Path, *, no_cache: bool = False) -> None:
        if not self.runner.have("devcontainer"):
            raise ToolMissing("devcontainer", hint="install via 'npm i -g @devcontainers/cli'")
        argv = ["devcontainer", "build", "--workspace-folder", str(worktree_root)]
        if no_cache:
            argv.append("--no-cache")
        log = logs_dir(worktree_root) / "build.log"
        self.runner.run_streaming(argv, prefix="[build] ", log_path=log)

    def up(self, worktree_root: Path) -> None:
        if not self.runner.have("devcontainer"):
            raise ToolMissing("devcontainer", hint="install via 'npm i -g @devcontainers/cli'")
        argv = ["devcontainer", "up", "--workspace-folder", str(worktree_root)]
        log = logs_dir(worktree_root) / "up.log"
        self.runner.run_streaming(argv, prefix="[up] ", log_path=log)

    # ------------------------------------------------------------------
    # attach (exec-replace; never returns)
    # ------------------------------------------------------------------
    def attach(self, worktree_root: Path, args: list[str]) -> None:
        """No args → drop into tmuxp. With args → exec verbatim with
        ``MCK_NO_TMUX=1`` so shell-init doesn't auto-exec tmuxp.
        """
        if not self.runner.have("devcontainer"):
            raise ToolMissing("devcontainer", hint="install via 'npm i -g @devcontainers/cli'")
        env: Optional[dict] = None
        argv: list[str]
        if not args:
            argv = [
                "devcontainer",
                "exec",
                "--workspace-folder",
                str(worktree_root),
                "bash",
                "-lc",
                _TMUX_LOAD_CMD,
            ]
        else:
            argv = [
                "devcontainer",
                "exec",
                "--workspace-folder",
                str(worktree_root),
                "env",
                "MCK_NO_TMUX=1",
                *args,
            ]
        self.runner.exec_replace(argv, env=env, cwd=worktree_root)

    def exec_capture(
        self,
        worktree_root: Path,
        args: list[str],
        *,
        check: bool = True,
    ):
        """Helper: run a command in the container, capture stdout. Used by
        higher-level verbs (kubeconfig, prepare-e2e) that need to read the
        result programmatically.
        """
        argv = [
            "devcontainer",
            "exec",
            "--workspace-folder",
            str(worktree_root),
            *args,
        ]
        return self.runner.run(argv, check=check)
