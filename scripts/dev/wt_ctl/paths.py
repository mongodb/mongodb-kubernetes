"""Worktree resolution + log directory helpers.

Walks up from cwd to a ``.git`` (file=worktree, dir=main repo) and confirms
the result is a worktree of the main repo. ``Runner`` is required for git
calls — domain modules never import subprocess.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from .errors import ExternalCommandFailed, NotInWorktree, WtCtlError
from .runner import Runner


@dataclass
class WorktreeRefs:
    """Where we are on disk + the main repo we belong to."""

    worktree_root: Path  # absolute path of the current worktree
    main_repo_root: Path  # absolute path of the main checkout
    branch: str  # current branch (or "HEAD" when detached)
    is_main: bool  # True iff worktree_root == main_repo_root

    @property
    def branch_dir(self) -> str:
        """Directory basename — what dc_select_network.sh stores in the
        registry, what compose normalizes to lowercase as project name.
        """
        return self.worktree_root.name


def _find_git_anchor(start: Path) -> Optional[Path]:
    """Walk up looking for a ``.git`` (dir or file). Stop at filesystem root."""
    cur = start.resolve()
    while True:
        if (cur / ".git").exists():
            return cur
        if cur.parent == cur:
            return None
        cur = cur.parent


def resolve_worktree(runner: Runner, cwd: Optional[Path] = None) -> WorktreeRefs:
    """Resolve the worktree the user is currently inside.

    Raises ``NotInWorktree`` (with a hint that cli.py renders) if cwd is not
    inside a worktree.
    """
    cwd = (cwd or Path.cwd()).resolve()
    anchor = _find_git_anchor(cwd)
    if anchor is None:
        raise NotInWorktree(
            cwd=str(cwd),
            hint="run inside a checkout, or use 'wt-ctl status --all' for a global view.",
        )
    # Resolve the toplevel for *this* checkout (which may be the worktree
    # itself, when .git is a file pointing at .git/worktrees/<name>).
    try:
        top = runner.run(
            ["git", "-C", str(anchor), "rev-parse", "--show-toplevel"],
        ).stdout.strip()
    except ExternalCommandFailed as exc:
        raise NotInWorktree(cwd=str(cwd), hint=str(exc)) from exc
    if not top:
        raise NotInWorktree(cwd=str(cwd))
    worktree_root = Path(top).resolve()

    # Find the common dir → tells us the main repo's .git directory location.
    common_dir = runner.run(
        ["git", "-C", str(worktree_root), "rev-parse", "--git-common-dir"],
    ).stdout.strip()
    common_path = (
        (worktree_root / common_dir).resolve() if not Path(common_dir).is_absolute() else Path(common_dir).resolve()
    )
    main_repo_root = common_path.parent  # `.git` parent → repo root

    # Branch name (HEAD when detached → return literal "HEAD").
    try:
        branch = runner.run(
            ["git", "-C", str(worktree_root), "rev-parse", "--abbrev-ref", "HEAD"],
        ).stdout.strip()
    except ExternalCommandFailed:
        branch = "HEAD"

    return WorktreeRefs(
        worktree_root=worktree_root,
        main_repo_root=main_repo_root,
        branch=branch,
        is_main=(worktree_root == main_repo_root),
    )


def logs_dir(worktree_root: Path) -> Path:
    """Setup-phase log directory used by orchestrator scripts."""
    return worktree_root / "logs" / "setup_worktree"


def state_dir(worktree_root: Path) -> Path:
    """Orchestrator state location (created lazily by `create`).

    Lives under ``.generated/`` alongside the other generated artefacts
    (``context.env``, ``current.kubeconfig``, …) so the worktree root stays
    clean and a single gitignore entry covers everything.
    """
    return worktree_root / ".generated" / "wt-ctl"


def create_artifacts_dir() -> Path:
    """Host-side staging directory for create logs / pid / exit codes.

    Deliberately *outside* any worktree: ``wt-ctl create --detach`` returns
    before the target worktree exists, and ``git worktree add`` refuses a
    non-empty target — so no bookkeeping file may be written under the
    target path before ``worktree_init`` has created it. Files are keyed by
    ``branch_dir`` (``<branch_dir>.log`` / ``.exit`` / ``.pid``).
    """
    return Path.home() / ".cache" / "mck-devc" / "create-logs"


def topology_marker(worktree_root: Path) -> Path:
    """Resolved single/multi topology for this worktree (``single``/``multi``).

    Mirrors ``.generated/.current_context``: sits under ``.generated/`` (so
    it survives ``--resume`` and is gitignored with the rest) and is owned by
    ``wt-ctl``. The requested topology wins (explicit flag > explicit context
    > marker > default multi), and when the checked-out context disagrees
    the orchestrator re-runs ``switch_context.sh`` so the generated files
    match before any later phase consumes them.
    """
    return worktree_root / ".generated" / ".current_topology"


def package_root() -> Path:
    """Absolute path of the wt_ctl package directory (used by the bash shim
    to set PYTHONPATH).
    """
    return Path(__file__).resolve().parent


def tooling_root() -> Path:
    """Absolute path of the checkout that CARRIES this tooling (wt_ctl,
    switch_context.sh, .devcontainer/...). Derived from this file's own
    location — wt-ctl is invoked by absolute path from its checkout and
    operates on the worktree found at cwd, which may be a checkout of a
    branch that has none of the tooling files.
    """
    # wt_ctl/ -> dev/ -> scripts/ -> repo root
    return package_root().parent.parent.parent


def devc_dir(worktree_root: Path) -> Path:
    """Per-worktree devcontainer render dir. Holds the rendered
    devcontainer.json, compose.yml copy, compose.generated.yml,
    compose.user.yml and the MCK_DEVC_* ``.env``. Lives under
    ``.generated/`` so worktrees of branches without the tooling stay
    clean (``.generated/`` is gitignored on master).
    """
    return worktree_root / ".generated" / "devcontainer"


def legacy_devc_dir(worktree_root: Path) -> Path:
    """Pre-portable location of the devcontainer env/override files
    (``<worktree>/.devcontainer``). Read-only fallback for migration."""
    return worktree_root / ".devcontainer"


def devc_env_dir(worktree_root: Path) -> Path:
    """Directory whose ``.env`` carries the MCK_DEVC_* block: the render
    dir when it has one, else the legacy ``.devcontainer`` fallback."""
    if (devc_dir(worktree_root) / ".env").is_file():
        return devc_dir(worktree_root)
    return legacy_devc_dir(worktree_root)
