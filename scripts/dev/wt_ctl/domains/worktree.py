"""git-worktree introspection (read-only) + thin wrappers around
``create_worktree.sh`` and ``git worktree remove``.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from ..errors import ExternalCommandFailed, WtCtlError
from ..runner import Runner
from ..state import GitState


@dataclass
class WorktreeEntry:
    path: Path
    branch: Optional[str]
    head: str
    is_main: bool
    detached: bool
    prunable: bool


class WorktreeDomain:
    def __init__(self, runner: Runner) -> None:
        self.runner = runner

    # ------------------------------------------------------------------
    # read
    # ------------------------------------------------------------------
    def list_worktrees(self, repo_root: Path) -> list[WorktreeEntry]:
        """Parse ``git worktree list --porcelain`` from the perspective of the
        main repo. ``repo_root`` should be the worktree root *or* main repo —
        git resolves either.
        """
        res = self.runner.run(
            ["git", "-C", str(repo_root), "worktree", "list", "--porcelain"],
        )
        out: list[WorktreeEntry] = []
        cur_path: Optional[Path] = None
        cur_head: str = ""
        cur_branch: Optional[str] = None
        cur_detached = False
        cur_prunable = False
        cur_is_main = False
        for line in res.stdout.splitlines() + [""]:
            if line.startswith("worktree "):
                cur_path = Path(line[len("worktree ") :]).resolve()
                cur_is_main = cur_path == repo_root.resolve()
            elif line.startswith("HEAD "):
                cur_head = line[len("HEAD ") :].strip()
            elif line.startswith("branch "):
                cur_branch = line[len("branch ") :].strip()
                if cur_branch.startswith("refs/heads/"):
                    cur_branch = cur_branch[len("refs/heads/") :]
            elif line == "detached":
                cur_detached = True
            elif line == "prunable":
                cur_prunable = True
            elif line == "":
                if cur_path is not None:
                    out.append(
                        WorktreeEntry(
                            path=cur_path,
                            branch=cur_branch,
                            head=cur_head,
                            is_main=cur_is_main,
                            detached=cur_detached,
                            prunable=cur_prunable,
                        )
                    )
                cur_path = None
                cur_branch = None
                cur_head = ""
                cur_detached = False
                cur_prunable = False
                cur_is_main = False
        return out

    def git_state(self, worktree_root: Path) -> GitState:
        """Branch + clean/dirty + ahead/behind summary."""
        try:
            branch = self.runner.run(
                ["git", "-C", str(worktree_root), "rev-parse", "--abbrev-ref", "HEAD"],
            ).stdout.strip()
        except ExternalCommandFailed:
            branch = "HEAD"
        # porcelain v1: any line == dirty
        status = self.runner.run(
            ["git", "-C", str(worktree_root), "status", "--porcelain"],
        ).stdout
        clean = status.strip() == ""
        upstream: Optional[str] = None
        ahead = 0
        behind = 0
        upstream_res = self.runner.run(
            [
                "git",
                "-C",
                str(worktree_root),
                "rev-parse",
                "--abbrev-ref",
                "--symbolic-full-name",
                "@{u}",
            ],
            check=False,
        )
        if upstream_res.rc == 0:
            upstream = upstream_res.stdout.strip()
            counts = self.runner.run(
                [
                    "git",
                    "-C",
                    str(worktree_root),
                    "rev-list",
                    "--left-right",
                    "--count",
                    "HEAD...@{u}",
                ],
                check=False,
            )
            if counts.rc == 0:
                parts = counts.stdout.strip().split()
                if len(parts) == 2:
                    try:
                        ahead = int(parts[0])
                        behind = int(parts[1])
                    except ValueError:
                        pass
        return GitState(
            branch=branch,
            clean=clean,
            ahead=ahead,
            behind=behind,
            upstream=upstream,
        )

    # ------------------------------------------------------------------
    # write
    # ------------------------------------------------------------------
    def add(self, repo_root: Path, branch: str, *, force: bool = False) -> None:
        """Wrap ``scripts/dev/create_worktree.sh``."""
        argv = [str(repo_root / "scripts/dev/create_worktree.sh")]
        if force:
            argv.append("-f")
        argv.append(branch)
        # create_worktree.sh assumes PROJECT_DIR is set; set it explicitly.
        env = {"PROJECT_DIR": str(repo_root)}
        self.runner.run_streaming(argv, env=env, cwd=repo_root)

    def remove(self, repo_root: Path, target_path: Path, *, force: bool = False) -> None:
        argv = ["git", "-C", str(repo_root), "worktree", "remove"]
        if force:
            argv.append("--force")
        argv.append(str(target_path))
        self.runner.run(argv)
