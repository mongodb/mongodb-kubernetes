"""The checks ``create`` makes first, before a step that is slow or costs an Evergreen host."""

from __future__ import annotations

import math
import os
import shutil
import sys
from pathlib import Path
from typing import Optional

from .errors import WtCtlError
from .runner import Runner

MIN_FREE_GIB = 10  # MCK_DEVC_MIN_FREE_GIB overrides it
BEHIND_WARN_COMMITS = 30
MASTER_REFS = ("upstream/master", "origin/master")  # as fetched: no network call


def disk_problem(free_gib: float, needed_gib: float) -> Optional[str]:
    if free_gib >= needed_gib:
        return None
    return (
        f"{free_gib:.1f} GiB free on the worktree filesystem, create needs {needed_gib:g} GiB "
        "(MCK_DEVC_MIN_FREE_GIB changes that). `docker system df` shows what docker holds."
    )


def behind_warning(behind: int, ref: str) -> Optional[str]:
    if behind <= BEHIND_WARN_COMMITS:
        return None
    return (
        f"this worktree is {behind} commits behind {ref}. The e2e images use the tag `latest`, which follows "
        "master, so a StatefulSet may never become ready: rebase if that happens"
    )


def check_disk(path: Path) -> None:
    try:
        needed = float(os.environ.get("MCK_DEVC_MIN_FREE_GIB", MIN_FREE_GIB))
    except ValueError:
        needed = -1
    if not 0 <= needed < math.inf:
        raise WtCtlError("MCK_DEVC_MIN_FREE_GIB must be a number of GiB, 0 or more")
    problem = disk_problem(shutil.disk_usage(path).free / 2**30, needed)
    if problem:
        raise WtCtlError(problem)


def target_commit(runner: Runner, checkout: Path, branch: str) -> str:
    """The commit of a worktree that ``create_worktree.sh`` has yet to make: the local branch, else the remote branch
    of exactly one remote (the script's rules), else the current commit, where a new branch starts."""
    try:
        local = runner.run(
            ["git", "rev-parse", "--verify", "--quiet", f"refs/heads/{branch}"], check=False, cwd=checkout
        )
        if local.rc == 0:
            return local.stdout.strip()
        remotes = runner.run(
            ["git", "for-each-ref", "--format=%(objectname)", f"refs/remotes/*/{branch}"], check=False, cwd=checkout
        )
        matches = remotes.stdout.split() if remotes.rc == 0 else []
        return matches[0] if len(matches) == 1 else "HEAD"
    except (WtCtlError, OSError):
        return "HEAD"


def preflight(runner: Runner, checkout: Path, target: str, disk_path: Optional[Path], needs_ssh_key: bool) -> None:
    """Refuse a disk with too little room (unless ``disk_path`` is None) and an Evergreen host without a key in the
    ssh agent. A worktree whose ``target`` commit is far behind master only gets a warning."""
    if disk_path is not None:
        check_disk(disk_path)
    if needs_ssh_key and runner.run(["ssh-add", "-l"], check=False).rc != 0:
        raise WtCtlError(
            "the ssh agent holds no key, and the devcontainer reaches an Evergreen host through it: "
            "`ssh-add ~/.ssh/evg-host` (the private key of your Evergreen key), then run create again"
        )
    for ref in MASTER_REFS:
        counted = runner.run(["git", "rev-list", "--count", f"{target}..{ref}"], check=False, cwd=checkout)
        if counted.rc == 0:
            warning = behind_warning(int(counted.stdout), ref)
            if warning:
                sys.stderr.write(f"[wt-ctl] warning: {warning}\n")
            break
