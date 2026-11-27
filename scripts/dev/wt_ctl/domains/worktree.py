"""git worktrees of the repository."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from ..runner import Runner


@dataclass
class WorktreeEntry:
    path: Path
    branch: Optional[str]  # None when detached
    prunable: bool = False


class WorktreeDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    def list_worktrees(self, repo_root: Path) -> list[WorktreeEntry]:
        """Every worktree of the repository, from ``git worktree list --porcelain``."""
        out = self.runner.run(["git", "-C", str(repo_root), "worktree", "list", "--porcelain"]).stdout
        entries: list[WorktreeEntry] = []
        for block in out.strip().split("\n\n"):
            fields = dict(line.split(" ", 1) if " " in line else (line, "") for line in block.splitlines())
            if "worktree" not in fields:
                continue
            branch = fields.get("branch", "").removeprefix("refs/heads/") or None
            entries.append(WorktreeEntry(Path(fields["worktree"]), branch, "prunable" in fields))
        return entries
