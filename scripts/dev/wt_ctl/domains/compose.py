"""Docker compose introspection — derive the project name and list services.

Project name = lowercase ``<branch_dir>_devcontainer`` (compose normalizes
project names to lowercase).
"""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

from ..errors import ToolMissing
from ..runner import Runner
from ..state import DevcState


def project_name_for(worktree_root: Path) -> str:
    return f"{worktree_root.name.lower()}_devcontainer"


class ComposeDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    # ------------------------------------------------------------------
    # read
    # ------------------------------------------------------------------
    def list_projects(self) -> list[dict]:
        try:
            res = self.runner.run(["docker", "compose", "ls", "--all", "--format", "json"], check=False)
        except ToolMissing:
            return []
        if res.rc != 0:
            return []
        try:
            return json.loads(res.stdout or "[]")
        except json.JSONDecodeError:
            return []

    def project_state(self, worktree_root: Path) -> Optional[DevcState]:
        """Return None if no compose stack matching this worktree exists."""
        proj = project_name_for(worktree_root)
        # Use `docker compose ls --all` to confirm the stack exists; then `ps`
        # for service detail. We deliberately don't load compose files here:
        # `compose ps -p <project>` works even when compose.generated.yml is
        # absent (e.g. during teardown).
        try:
            res = self.runner.run(
                ["docker", "compose", "-p", proj, "ps", "--all", "--format", "json"],
                check=False,
            )
        except ToolMissing:
            return None
        if res.rc != 0:
            return None
        # `docker compose ps --format json` emits ONE json object per line,
        # not a JSON array.
        services: list[dict] = []
        for line in res.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(obj, list):
                services.extend(obj)
            else:
                services.append(obj)
        if not services:
            return None
        running = sum(1 for s in services if s.get("State") == "running")
        # Aggregate: stack is "running" if every service is up.
        if running == len(services):
            stack_state = "running"
        elif running == 0:
            stack_state = "exited"
        else:
            stack_state = "partial"
        # Image age: report the age of the most-recently-created service image
        # (newest CreatedAt), i.e. how long ago the stack was last (re)built.
        image_age = self._newest_age(services)
        return DevcState(
            project=proj,
            state=stack_state,
            services_running=running,
            services_total=len(services),
            image_age=image_age,
        )

    def _newest_age(self, services: list[dict]) -> Optional[str]:
        # Parse compose CreatedAt strings like "2026-05-10 16:45:39 +0200 CEST".
        # Pull the date+time+offset portion and ignore trailing tz abbrev.
        newest: Optional[datetime] = None
        for svc in services:
            raw = svc.get("CreatedAt") or ""
            if not raw:
                continue
            try:
                parts = raw.split(" ")
                if len(parts) >= 3:
                    iso = " ".join(parts[:3])
                else:
                    iso = raw
                dt = datetime.strptime(iso, "%Y-%m-%d %H:%M:%S %z")
            except ValueError:
                continue
            if newest is None or dt > newest:
                newest = dt
        if newest is None:
            return None
        delta = datetime.now(timezone.utc) - newest
        secs = int(delta.total_seconds())
        if secs < 60:
            return f"{secs}s"
        if secs < 3600:
            return f"{secs // 60}m"
        if secs < 86400:
            return f"{secs // 3600}h"
        return f"{secs // 86400}d"

    # ------------------------------------------------------------------
    # write
    # ------------------------------------------------------------------
    def down(self, worktree_root: Path) -> None:
        proj = project_name_for(worktree_root)
        compose = worktree_root / ".devcontainer" / "compose.yml"
        gen = worktree_root / ".devcontainer" / "compose.generated.yml"
        argv = ["docker", "compose", "-p", proj, "-f", str(compose)]
        if gen.exists():
            argv += ["-f", str(gen)]
        argv += ["down"]
        self.runner.run_streaming(argv)
