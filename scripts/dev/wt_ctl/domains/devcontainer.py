"""Wraps the ``devcontainer`` CLI — up / build / exec / attach."""

from __future__ import annotations

import os
from pathlib import Path
from typing import Optional

from ..envfile import read_env_file
from ..errors import ToolMissing
from ..paths import devc_dir, logs_dir, tooling_root
from ..runner import Runner
from .compose import project_name_for

_TMUX_LOAD_CMD = "exec tmuxp load -y /mck-tooling/.devcontainer/tmuxp/mck.yaml"


def compose_env(worktree_root: Path) -> dict:
    """Env overrides for every devcontainer-CLI call on this worktree.

    * ``COMPOSE_PROJECT_NAME`` — the CLI derives it from the config's
      directory when unset, which is ``devcontainer`` for every worktree
      once the config is rendered under ``.generated/devcontainer``, so all
      stacks would collide on one project.
    * ``MCK_DEVC_*`` — re-asserted from THIS worktree's ``.env``. The wt-ctl
      shim sources the invoking checkout's context, and process env beats
      the project-directory ``.env`` in compose interpolation, so without
      this a stack created from another checkout gets that checkout's
      subnet ("Pool overlaps with other one on this address space").
    """
    env = {"COMPOSE_PROJECT_NAME": project_name_for(worktree_root)}
    for key, value in read_env_file(devc_dir(worktree_root) / ".env").items():
        if key.startswith("MCK_DEVC_"):
            env[key] = value
    return env


def _config_args(worktree_root: Path) -> list[str]:
    """Point the devcontainer CLI at the per-worktree RENDERED config
    (``<wt>/.generated/devcontainer/devcontainer.json``) so worktrees
    without the devcontainer tooling checked in still work. The compose
    files referenced by that config live next to it."""
    return ["--config", str(devc_dir(worktree_root) / "devcontainer.json")]


class DevcontainerDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    # ------------------------------------------------------------------
    # build / up
    # ------------------------------------------------------------------
    def ensure_rendered(self, worktree_root: Path) -> None:
        """Render the per-worktree devcontainer config if it isn't there
        yet (idempotent; the orchestrator also runs this as a phase)."""
        if not (devc_dir(worktree_root) / "devcontainer.json").is_file():
            script = tooling_root() / ".devcontainer" / "scripts" / "initialize.sh"
            self.runner.run(["bash", str(script)], cwd=worktree_root)

    def build(self, worktree_root: Path, *, no_cache: bool = False) -> None:
        if not self.runner.have("devcontainer"):
            raise ToolMissing("devcontainer", hint="install via 'npm i -g @devcontainers/cli'")
        self.ensure_rendered(worktree_root)
        argv = ["devcontainer", "build", "--workspace-folder", str(worktree_root), *_config_args(worktree_root)]
        if no_cache:
            argv.append("--no-cache")
        log = logs_dir(worktree_root) / "build.log"
        self.runner.run_streaming(argv, prefix="[build] ", log_path=log, env=compose_env(worktree_root))

    def up(self, worktree_root: Path) -> None:
        if not self.runner.have("devcontainer"):
            raise ToolMissing("devcontainer", hint="install via 'npm i -g @devcontainers/cli'")
        self.ensure_rendered(worktree_root)
        argv = ["devcontainer", "up", "--workspace-folder", str(worktree_root), *_config_args(worktree_root)]
        log = logs_dir(worktree_root) / "up.log"
        self.runner.run_streaming(argv, prefix="[up] ", log_path=log, env=compose_env(worktree_root))

    # ------------------------------------------------------------------
    # attach (exec-replace; never returns)
    # ------------------------------------------------------------------
    def attach(self, worktree_root: Path, args: list[str]) -> None:
        """No args → drop into tmuxp. With args → exec verbatim with
        ``MCK_NO_TMUX=1`` so shell-init doesn't auto-exec tmuxp.
        """
        if not self.runner.have("devcontainer"):
            raise ToolMissing("devcontainer", hint="install via 'npm i -g @devcontainers/cli'")
        env: Optional[dict] = compose_env(worktree_root)
        argv: list[str]
        if not args:
            argv = [
                "devcontainer",
                "exec",
                "--workspace-folder",
                str(worktree_root),
                *_config_args(worktree_root),
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
                *_config_args(worktree_root),
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
            *_config_args(worktree_root),
            *args,
        ]
        return self.runner.run(argv, check=check, env=compose_env(worktree_root))
