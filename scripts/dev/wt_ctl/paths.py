"""Where the checkout and the main repository are, and where a worktree keeps its files."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from .domains.worktree import WorktreeDomain
from .errors import ExternalCommandFailed, NotInWorktree
from .runner import Runner


@dataclass
class WorktreeRefs:
    worktree_root: Path
    main_repo_root: Path
    branch: str  # "HEAD" when detached

    @property
    def branch_dir(self) -> str:
        return self.worktree_root.name


def branch_dir_for(branch: str) -> str:
    """The directory of a branch's worktree: ``create_worktree.sh`` turns ``a/b`` into ``a_b``."""
    return branch.replace("/", "_")


def worktree_path_for(runner: Runner, refs: WorktreeRefs, branch: str) -> Path:
    """Where ``branch`` is checked out, so that ``create`` adopts a worktree made by hand; else the sibling of this
    checkout that ``create`` makes."""
    for entry in WorktreeDomain(runner).list_worktrees(refs.main_repo_root):
        if entry.branch == branch:
            return entry.path
    return refs.worktree_root.parent / branch_dir_for(branch)


def resolve_worktree(runner: Runner, cwd: Optional[Path] = None) -> WorktreeRefs:
    """The checkout that contains ``cwd`` and the main repository it belongs to."""
    cwd = (cwd or Path.cwd()).resolve()
    try:
        top, common, branch = runner.run(
            ["git", "-C", str(cwd), "rev-parse", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD"]
        ).stdout.split("\n")[:3]
    except ExternalCommandFailed as exc:
        raise NotInWorktree(f"{cwd} is not inside a git checkout") from exc
    worktree_root = Path(top).resolve()
    return WorktreeRefs(worktree_root, (worktree_root / common).resolve().parent, branch)


def logs_dir(worktree_root: Path) -> Path:
    return worktree_root / "logs" / "setup_worktree"


def state_dir(worktree_root: Path) -> Path:
    return worktree_root / ".generated" / "wt-ctl"


def shared_state_dir(main_repo_root: Path) -> Path:
    """The one directory all checkouts of the repository share, for the index lock."""
    return state_dir(main_repo_root)


def devc_dir(worktree_root: Path) -> Path:
    """Compose and the devcontainer CLI read ``.env`` from the directory of ``compose.yml``."""
    return worktree_root / ".devcontainer"
