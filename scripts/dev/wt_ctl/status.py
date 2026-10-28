"""``wt-ctl status``: the worktrees of this repository and what is running of each."""

from __future__ import annotations

from .domains import kfp
from .domains.devcontainer import DevcontainerDomain
from .domains.evg import pinned_host_name
from .domains.kind import KindDomain
from .domains.network import read_pin, worktree_prefix
from .domains.registry_auth import describe, read_auths, store_path
from .domains.worktree import WorktreeDomain
from .paths import WorktreeRefs
from .runner import Runner


def status(runner: Runner, refs: WorktreeRefs) -> None:
    clusters = runner.run(["kind", "get", "clusters"], check=False).stdout.split()
    worktrees = []
    for entry in WorktreeDomain(runner).list_worktrees(refs.main_repo_root):
        pin = entry.path.is_dir() and read_pin(entry.path)
        if pin:
            worktrees.append((pin.index, entry))
    url = kfp.daemon_url()
    if url is not None and not kfp.is_running(url):
        print(f"kfp is not running at {url}: host shells do not resolve *.svc.cluster.local")
    if not worktrees:
        print("no worktrees: `wt-ctl create --local-kind <branch>` makes one")
    for index, entry in sorted(worktrees, key=lambda s: s[0]):
        prefix = worktree_prefix(index)
        kind = len([c for c in clusters if c.startswith(prefix)])
        devc = "up" if DevcontainerDomain(runner, entry.path).is_running() else "down"
        registry = describe(read_auths(store_path(entry.path)))[1]
        host = pinned_host_name(entry.path)
        health = "" if host else f"  health: {KindDomain(runner).unhealthy(entry.path, prefix) or 'ok'}"
        print(
            f"{prefix:<8} {entry.branch or '(detached)':<40} "
            f"{'evergreen host: ' + host if host else f'kind clusters: {kind}'}{health}  devcontainer: {devc}  "
            f"registry: {registry}  {entry.path}"
        )
