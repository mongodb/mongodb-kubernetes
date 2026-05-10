"""Evergreen host domain.

We rely on the ``evergreen`` CLI directly (the upstream JSON is the cleanest
contract) and fall back to wrapping ``scripts/dev/evg_host.sh`` for the
kubeconfig flow that already encapsulates yq + kfp registration.
"""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Optional

from ..errors import ExternalCommandFailed, ToolMissing
from ..runner import Runner
from ..state import EvgHostState


class EvgDomain:
    def __init__(self, runner: Runner, repo_root: Path) -> None:
        self.runner = runner
        self.repo_root = repo_root
        # evg_host.sh derives its kubeconfig path from PROJECT_DIR; we pass
        # the worktree as PROJECT_DIR when invoking the script.

    # ------------------------------------------------------------------
    def list_hosts(self, *, mine: bool = True) -> list[dict]:
        if not self.runner.have("evergreen"):
            raise ToolMissing("evergreen", hint="https://spruce.mongodb.com/preferences/cli")
        argv = ["evergreen", "host", "list", "--json"]
        if mine:
            argv.insert(-1, "--mine")
        res = self.runner.run(argv, check=False)
        if res.rc != 0:
            return []
        try:
            data = json.loads(res.stdout or "[]")
        except json.JSONDecodeError:
            return []
        return data if isinstance(data, list) else []

    def find_by_name(self, name: str) -> Optional[dict]:
        if not name:
            return None
        for h in self.list_hosts(mine=True):
            if h.get("name") == name:
                return h
        # fall back to global list (some setups don't tag hosts as --mine
        # consistently) — the project is shared anyway.
        for h in self.list_hosts(mine=False):
            if h.get("name") == name:
                return h
        return None

    # ------------------------------------------------------------------
    def state_for(self, worktree_root: Path) -> Optional[EvgHostState]:
        """Resolve the EVG host pinned to this worktree.

        Pin source order:
          1. ``.generated/.current-evg-host`` (set by evg_prepare.sh).
          2. By convention the EVG host display name == ``branch_dir`` —
             we fall back to that if no pin file exists.
        """
        pin = worktree_root / ".generated" / ".current-evg-host"
        if pin.is_file():
            name = pin.read_text().strip()
        else:
            name = worktree_root.name
        if not name:
            return None
        try:
            host = self.find_by_name(name)
        except ToolMissing:
            return EvgHostState(
                name=name, id=None, status=None,
                host_name=None, expires_in=None, ssh=None,
            )
        if host is None:
            return EvgHostState(
                name=name, id=None, status="not-found",
                host_name=None, expires_in=None, ssh=None,
            )
        host_name = host.get("host_name") or None
        user = host.get("user") or "ubuntu"
        ssh = f"ssh {user}@{host_name}" if host_name else None
        return EvgHostState(
            name=host.get("name"),
            id=host.get("id"),
            status=host.get("status"),
            host_name=host_name,
            expires_in=None,  # the --mine JSON doesn't include expiration
            ssh=ssh,
        )

    # ------------------------------------------------------------------
    def terminate(self, host_id: str) -> str:
        return self.runner.run(
            ["evergreen", "host", "terminate", "--host", host_id],
        ).stdout

    def extend(self, host_id: str, hours: int) -> str:
        return self.runner.run(
            ["evergreen", "host", "modify", "--extend-by", str(hours), "--host", host_id],
        ).stdout

    # ------------------------------------------------------------------
    def get_kubeconfig(self, worktree_root: Path, *, no_fetch: bool = False) -> str:
        """Wrap ``scripts/dev/evg_host.sh get-kubeconfig`` (so all the yq +
        kfp PATCH logic stays in one place — the workhorse).
        """
        env = self._evg_env(worktree_root)
        if env is None:
            return "(no EVG_HOST_NAME pinned; skipping)"
        argv = [
            str(self.repo_root / "scripts/dev/evg_host.sh"),
            "get-kubeconfig",
        ]
        if no_fetch:
            argv.append("--no-fetch")
        res = self.runner.run(argv, env=env, cwd=worktree_root)
        return res.stdout

    # ------------------------------------------------------------------
    def _evg_env(self, worktree_root: Path) -> Optional[dict]:
        """Build env for evg_host.sh: PROJECT_DIR + EVG_HOST_NAME."""
        host_pin = worktree_root / ".generated" / ".current-evg-host"
        host_name = host_pin.read_text().strip() if host_pin.is_file() else worktree_root.name
        if not host_name:
            return None
        return {
            "PROJECT_DIR": str(worktree_root),
            "EVG_HOST_NAME": host_name,
        }
