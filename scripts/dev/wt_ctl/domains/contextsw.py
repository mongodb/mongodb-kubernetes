"""Context-switch helpers.

Read: parse ``.generated/.current_context``.
Write: run ``scripts/dev/switch_context.sh <ctx>`` directly.
"""

from __future__ import annotations

from pathlib import Path
from typing import Optional

from ..paths import tooling_root
from ..runner import Runner


class ContextDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    def current(self, worktree_root: Path) -> Optional[str]:
        f = worktree_root / ".generated" / ".current_context"
        if not f.is_file():
            return None
        try:
            return f.read_text().strip() or None
        except OSError:
            return None

    def switch(self, worktree_root: Path, ctx: str) -> None:
        # Run the TOOLING's switch_context.sh against the target worktree
        # (cwd + PROJECT_DIR): the worktree may not carry the devcontainer
        # tooling, and the tooling version emits both the per-side files
        # and the master-compat context.export.env.
        script = tooling_root() / "scripts" / "dev" / "switch_context.sh"
        self.runner.run_streaming(
            [str(script), ctx],
            prefix="[ctx] ",
            cwd=worktree_root,
            env={"PROJECT_DIR": str(worktree_root)},
        )

    def list_available(self, repo_root: Path) -> list[str]:
        ctx_dir = repo_root / "scripts" / "dev" / "contexts"
        if not ctx_dir.is_dir():
            return []
        out = []
        for child in sorted(ctx_dir.iterdir()):
            if child.is_file() and not child.name.startswith("."):
                # Filter out non-context helpers; the contexts dir mixes
                # ``site-context`` / ``root-context`` / ``private-context``
                # with the actual context files. The rendered contexts have
                # no dash-prefix in practice; the helpers are dash-suffixed.
                if child.name.endswith("-context"):
                    continue
                out.append(child.name)
        return out
