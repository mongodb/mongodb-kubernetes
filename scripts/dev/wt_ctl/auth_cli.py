"""``wt-ctl auth``: show and renew the registry credentials of this worktree."""

from __future__ import annotations

import argparse
import contextlib
import sys
from pathlib import Path

from .domains.devcontainer import DevcontainerDomain
from .domains.evg import pinned_host_name
from .domains.registry_auth import (
    RENEW_BELOW_S,
    describe,
    ensure_store,
    read_auths,
    read_container_copy,
    read_host_copy,
    roll,
    seconds_left,
    store_path,
)
from .envfile import inside_container
from .errors import WtCtlError
from .paths import WorktreeRefs, logs_dir
from .runner import Runner


def run_auth(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    if args.auth_cmd == "refresh":
        return refresh(runner, refs.worktree_root, force=args.force)
    return status(runner, refs.worktree_root)


def status(runner: Runner, wt: Path) -> int:
    """Print every copy of the credentials with its time left; the exit code is the worst of them (0, 1 or 3)."""
    rows = [("worktree store", read_auths(store_path(wt)), "")]
    if inside_container():
        rows.append(("devcontainer copy", read_auths(Path.home() / ".docker" / "config.json"), ""))
    else:
        rows.append(("devcontainer copy", read_container_copy(runner, DevcontainerDomain(runner, wt)), "not running"))
        if pinned_host_name(wt):
            rows.append(("evergreen host copy", read_host_copy(runner, wt), "unreachable"))
    worst = 0
    for label, auths, missing in rows:
        code, text = (0, missing) if auths is None else describe(auths)
        print(f"{label:<18} {text}")
        worst = max(worst, code)
    return worst


def refresh(runner: Runner, wt: Path, *, force: bool = False) -> int:
    """Renew the token when under 4 hours are left (or with ``force``) and roll it to the devcontainer and the
    pull secrets."""
    if inside_container():
        raise WtCtlError(
            "auth refresh runs on the host, not in the container: run `scripts/dev/wt-ctl auth refresh` there"
        )
    minted = ensure_store(runner, wt, force=force)
    print("minted a new ECR token" if minted else "the stored ECR token has time left")
    if not roll(runner, wt, logs_dir(wt) / "auth_refresh.log"):
        print("the devcontainer is not running: its copy and the pull secrets are not updated")
    return status(runner, wt)


def renew_for_attach(runner: Runner, wt: Path) -> None:
    """Before ``attach``: renew when the store has under 4 hours left. A failure is a warning; attach goes on."""
    left = seconds_left(read_auths(store_path(wt)))
    if inside_container() or (left is not None and left >= RENEW_BELOW_S):
        return
    try:
        with contextlib.redirect_stdout(sys.stderr):  # stdout belongs to the command that attach runs
            refresh(runner, wt)
    except WtCtlError as exc:
        sys.stderr.write(f"[wt-ctl] warning: registry credentials not renewed: {exc.render()}\n")
