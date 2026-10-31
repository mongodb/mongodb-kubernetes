"""``wt-ctl delete``: take a worktree apart, one opt-in target at a time.

A failed target stops the delete before the next one, so the git worktree, which holds the index and
state of the prefix, stays until everything else is gone. Run it again to continue. Git branches are never deleted.
"""

from __future__ import annotations

import argparse
import sys

from .domains.devcontainer import DevcontainerDomain
from .domains.evg import EvgDomain
from .domains.kfp import unregister_worktree
from .domains.kind import KindDomain
from .domains.network import read_pin, worktree_prefix
from .errors import WtCtlError
from .paths import WorktreeRefs, worktree_path_for
from .runner import Runner


def delete(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> None:
    targets = {name: args.all or getattr(args, name) for name in ("om", "devcontainer", "evg", "clusters", "worktree")}
    if not any(targets.values()):
        raise WtCtlError("nothing to delete: pass --all, or some of --om --devcontainer --evg --clusters --worktree")
    wt = worktree_path_for(runner, refs, args.branch)
    pin = read_pin(wt)
    if not wt.is_dir() or pin is None:
        raise WtCtlError(f"{wt} is not a worktree created by wt-ctl")
    if wt == refs.worktree_root:
        raise WtCtlError("run delete from another checkout than the one it deletes")
    # Checked first: git refuses at the last step, after the clusters and the containers are already gone.
    if targets["worktree"] and not args.force and runner.run(["git", "-C", str(wt), "status", "--porcelain"]).stdout:
        raise WtCtlError(f"{wt} has uncommitted changes, nothing was deleted: commit or stash them, or pass --force")

    def step(name: str) -> bool:
        if targets[name]:
            sys.stderr.write(f"[wt-ctl] delete {name}\n")
        return targets[name]

    if step("om"):
        # Scoped by the worktree's namespace prefix, so only this worktree's Cloud QA projects go. Missing Cloud QA
        # credentials must not keep the rest of the worktree alive.
        try:
            runner.stream([str(wt / "scripts" / "dev" / "delete_om_projects.sh")], cwd=wt)
        except WtCtlError as exc:
            sys.stderr.write(f"[wt-ctl] warning: Ops Manager projects not deleted: {exc.render()}\n")
    if step("devcontainer"):
        # A worktree that is gone must not stay in the shared kfp daemon, but a daemon that is down is no reason
        # to keep it.
        try:
            removed = unregister_worktree(wt)
            sys.stderr.write(f"[wt-ctl] kfp: unregistered {', '.join(removed) or 'nothing'}\n")
        except WtCtlError as exc:
            sys.stderr.write(f"[wt-ctl] warning: kfp contexts not unregistered: {exc.render()}\n")
        DevcontainerDomain(runner, wt).down()
    if step("evg"):
        EvgDomain(runner).leave(wt, pin.index)
    if step("clusters"):
        KindDomain(runner).delete_clusters(worktree_prefix(pin.index))
    if step("worktree"):
        argv = ["git", "-C", str(refs.main_repo_root), "worktree", "remove", str(wt)]
        runner.run(argv + (["--force"] if args.force else []))
