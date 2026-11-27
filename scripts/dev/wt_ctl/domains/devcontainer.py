"""The devcontainer of a worktree: the ``devcontainer`` CLI and the compose project behind it."""

from __future__ import annotations

import re
import sys
from pathlib import Path

from ..errors import ExternalCommandFailed, WtCtlError
from ..runner import Runner

# What every in-container command starts with: bash -l alone does not load the checkout's env.
IN_DEVENV = "set -Eeou pipefail; cd /workspace; . /workspace/scripts/dev/devenv; "


def project_name(worktree: Path) -> str:
    """The compose project of a worktree. The CLI derives its own name from the directory, and differs for
    a dot or an uppercase letter, so every call passes this one."""
    return re.sub(r"[^a-z0-9_-]", "", worktree.name.lower()) + "_devcontainer"


def own_volumes(project: str, names: list[str]) -> list[str]:
    """The volumes of ``names`` (already filtered by the compose project label) that belong to ``project`` alone.

    The go, uv and helm cache volumes are shared by all worktrees and carry the label of whichever worktree created
    them first, so only names that start with the project's own prefix count."""
    return [name for name in names if name.startswith(f"{project}_")]


class DevcontainerDomain:
    def __init__(self, runner: Runner, worktree: Path) -> None:
        self.runner = runner
        self.worktree = worktree
        self.project = project_name(worktree)
        self.env = {"COMPOSE_PROJECT_NAME": self.project}

    def _cli(self, verb: str, *args: str) -> list[str]:
        if not self.runner.have("devcontainer"):
            raise WtCtlError("devcontainer not found on PATH: npm i -g @devcontainers/cli")
        return ["devcontainer", verb, "--workspace-folder", str(self.worktree), *args]

    def _compose(self) -> list[str]:
        devc = self.worktree / ".devcontainer"
        argv = ["docker", "compose", "-p", self.project]
        for f in (devc / "compose.yml", devc / "compose.generated.yml"):
            if f.is_file():
                argv += ["-f", str(f)]
        return argv

    def prepare_and_up(self, log: Path) -> None:
        """Write the compose override files on the host, build the image and start the worktree."""
        init = ["bash", str(self.worktree / ".devcontainer" / "scripts" / "initialize.sh")]
        self.runner.stream(init, log_path=log, cwd=self.worktree)
        self.runner.stream(self._cli("build"), log_path=log, cwd=self.worktree, env=self.env)
        before = self.container_id()
        try:
            self.runner.stream(self._cli("up"), log_path=log, cwd=self.worktree, env=self.env)
        except ExternalCommandFailed:
            # The CLI runs onCreateCommand once per container and does not run it again for a container whose first run
            # failed, which would stay without its venv. Remove a container that this up created or replaced, so that
            # the next create starts a new one. A container that was there before stays.
            try:
                if self.container_id() not in ("", before):
                    self.runner.run([*self._compose(), "rm", "-sf", "devcontainer"], cwd=self.worktree, env=self.env)
            except (WtCtlError, OSError) as exc:
                sys.stderr.write(f"[wt-ctl] warning: could not remove the failed devcontainer: {exc}\n")
            raise

    def bash(self, script: str, log: Path) -> None:
        """Run ``script`` in the container with the checkout's env loaded."""
        argv = self._cli("exec", "bash", "-lc", IN_DEVENV + script)
        self.runner.stream(argv, log_path=log, cwd=self.worktree, env=self.env)

    def attach(self, args: list[str]) -> None:
        """A shell in the container (the tmux layout), or ``args`` run there. Does not return."""
        if args:
            argv = self._cli("exec", "env", "MCK_NO_TMUX=1", *args)
        else:
            if not sys.stdin.isatty():
                raise WtCtlError("attach without a command needs a terminal: pass the command after `--`")
            argv = self._cli("exec", "bash", "-lc", "exec tmuxp load -y /workspace/.devcontainer/tmuxp/mck.yaml")
        self.runner.exec_replace(argv, cwd=self.worktree, env=self.env)

    def down(self) -> None:
        """Stop the worktree and remove its images and its own volumes, not the shared caches."""
        self.runner.stream(
            [*self._compose(), "down", "--remove-orphans", "--rmi", "local"], cwd=self.worktree, env=self.env
        )
        # Keep the worktree so delete can retry a failed volume cleanup.
        labelled = self.runner.run(
            ["docker", "volume", "ls", "-q", "--filter", f"label=com.docker.compose.project={self.project}"]
        ).stdout.split()
        volumes = own_volumes(self.project, labelled)
        if volumes:
            self.runner.run(["docker", "volume", "rm", *volumes])

    def container_id(self, service: str = "devcontainer") -> str:
        """The id of the running container of ``service``, empty when the worktree is down."""
        res = self.runner.run(
            ["docker", "compose", "-p", self.project, "ps", "-q", service], check=False, cwd=self.worktree
        )
        return res.stdout.strip() if res.rc == 0 else ""

    def is_running(self) -> bool:
        return bool(self.container_id())
