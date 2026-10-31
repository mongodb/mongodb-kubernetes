"""``wt-ctl create``: a git worktree with a prefix of its own, its kind clusters and its devcontainer.

The clusters run on an Evergreen host, which ``create`` spawns or reuses, or with ``--local-kind`` on this machine.

Every step is idempotent, so a create that failed or was interrupted continues when it runs again for the
same branch. The files a worktree keeps of its own are ``.generated/``, with its ``worktree-context``, and
``.devcontainer/.env``.
"""

from __future__ import annotations

import argparse
import shutil
import sys
from pathlib import Path
from typing import Optional

from .domains.devcontainer import DevcontainerDomain
from .domains.evg import EvgDomain, pinned_host_name
from .domains.kfp import register_worktree
from .domains.kind import KindDomain
from .domains.kubeconfig import need_yaml, refresh
from .domains.network import (
    NetworkDomain,
    read_pin,
    worktree_prefix,
    worktree_values,
    write_pin,
    write_worktree_context,
)
from .domains.registry_auth import copy_to_container, ensure_store, push_to_host, store_path
from .envfile import inside_container, read_env_file
from .errors import WtCtlError
from .paths import WorktreeRefs, devc_dir, logs_dir, shared_state_dir, worktree_path_for
from .runner import Runner

DEFAULT_SINGLE_CONTEXT = "root-context"
DEFAULT_MULTI_CONTEXT = "e2e_multi_cluster_kind"

# create_worktree.sh copies the invoking checkout's .generated/, and with it that worktree's clusters, registry
# store and kubeconfigs. A new worktree starts without them.
INHERITED_STATE = (
    ".generated/registry-auth",
    ".generated/wt-ctl",
    ".generated/kind-kubeconfigs",
    ".generated/current.kubeconfig",
    ".generated/current.devc.kubeconfig",
    ".generated/multicluster_kubeconfig",
    ".generated/multicluster.devc.kubeconfig",
    ".generated/.current_topology",
    ".generated/.current-evg-host",
    ".generated/.current-evg-host-address",
    ".generated/ssh",
)


def resolve_topology(
    explicit: Optional[bool], context: Optional[str], context_is_multi: bool, recorded: Optional[bool]
) -> tuple[bool, Optional[str]]:
    """Single or multi cluster, and the context to switch to when the current one does not match.

    The order is: the flag, the context named with ``--context``, the topology recorded by an earlier create,
    multi cluster."""
    if explicit is not None and context is not None and explicit != context_is_multi:
        flag = "--multi-cluster" if explicit else "--single-cluster"
        raise WtCtlError(f"{flag} contradicts --context {context}, which is not of that kind: drop one of them")
    if explicit is not None:
        multi = explicit
    elif context is not None:
        multi = context_is_multi
    else:
        multi = True if recorded is None else recorded
    switch_to = None if multi == context_is_multi else (DEFAULT_MULTI_CONTEXT if multi else DEFAULT_SINGLE_CONTEXT)
    return multi, switch_to


def evg_host_name(args: argparse.Namespace, wt: Path) -> str:
    """The Evergreen host of the worktree, empty for one with local kind clusters. A rerun keeps what the worktree
    is: one that has an index but no host stays local, so that forgetting ``--local-kind`` cannot spawn a host."""
    if args.local_kind:
        return ""
    return args.evg_host_name or pinned_host_name(wt) or ("" if read_pin(wt) else wt.name)


class Create:
    def __init__(self, runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> None:
        self.runner = runner
        self.refs = refs
        self.args = args
        self.host = refs.worktree_root
        self.wt = worktree_path_for(runner, refs, args.branch)
        self.evg_name = evg_host_name(args, self.wt)
        # The worktree's own kubeconfig, so that scripts do not touch the kubectl context of the invoking checkout, and
        # its own registry store, which setup_kind_cluster.sh mounts into the kind nodes.
        self.env = {
            "PROJECT_DIR": str(self.wt),
            "KUBECONFIG": str(self.wt / ".generated" / "current.kubeconfig"),
            "MCK_KIND_REGISTRY_AUTH_DIR": str(store_path(self.wt).parent),
        }

    def run(self) -> None:
        need_yaml()
        self.step("worktree", self.create_worktree)
        self.step("prefix", self.write_prefix)
        multi = self.step("context", self.switch_context)
        devc = DevcontainerDomain(self.runner, self.wt)
        # The nodes mount the store, so it needs a token before they exist.
        self.step("registry credentials", lambda: ensure_store(self.runner, self.wt))
        if not self.evg_name:
            if not self.args.skip_recreate or self.unhealthy_clusters():
                self.step("kind clusters", lambda: self.recreate_clusters(multi))
        elif not self.args.skip_evg:
            self.step("evergreen host", self.prepare_host)
            self.step("kind clusters", lambda: self.recreate_on_host(multi))
        self.step("kubeconfig", self.refresh_kubeconfig)
        self.step("devcontainer", lambda: self.start_devcontainer(devc))
        if not self.args.skip_prepare_e2e:
            self.step("prepare-local-e2e", lambda: self.prepare_e2e(devc))
        self.step("operator", lambda: self.start_operator(devc))
        sys.stderr.write(f"[wt-ctl] done: {self.wt}\n")

    def step(self, name: str, action):
        sys.stderr.write(f"[wt-ctl] {name}\n")
        return action()

    def log(self, name: str) -> Path:
        return logs_dir(self.wt) / f"{name}.log"

    def create_worktree(self) -> None:
        """Make the worktree, or adopt one made by hand with ``git worktree add``: ``init_worktree.sh`` copies in the
        private-context, ``bin/`` and a context from the invoking checkout, or from the main checkout when ``create``
        runs inside the worktree itself, and builds the venv, as for a new worktree."""
        private = self.wt / "scripts" / "dev" / "contexts" / "private-context"
        if private.is_file():
            if not (self.wt / ".generated" / ".current_context").is_file():
                raise WtCtlError(
                    f"no context in {self.wt / '.generated'}: run `make switch context=<context>` there first"
                )
            return
        source = self.host if self.host != self.wt else self.refs.main_repo_root
        contexts = source / "scripts" / "dev" / "contexts"
        if not (contexts / "private-context").is_file():
            raise WtCtlError(f"{contexts / 'private-context'} not found: copy private-context-template and fill it in")
        if not (source / ".generated" / ".current_context").is_file():
            raise WtCtlError(f"no context in {source / '.generated'}: run `make switch context=<context>` there first")
        log = logs_dir(self.host) / f"worktree-{self.wt.name}.log"
        scripts = source / "scripts" / "dev"
        # The scripts derive the new path from PROJECT_DIR: the source checkout, as the worktree's own may not exist yet.
        env = {**self.env, "PROJECT_DIR": str(source)}
        if self.wt.is_dir():
            argv = [str(scripts / "init_worktree.sh"), str(self.wt), str(source)]
        else:
            argv = [str(scripts / "create_worktree.sh"), self.args.branch]
        self.runner.stream(argv, log_path=log, cwd=source, env=env)
        for relative in INHERITED_STATE:
            path = self.wt / relative
            shutil.rmtree(path) if path.is_dir() else path.unlink(missing_ok=True)
        # Like private-context (init_worktree.sh copies that one), the gitignored compose overrides belong to the user.
        overrides = devc_dir(source) / "compose.user.yml"
        if overrides.is_file() and source != self.wt:
            shutil.copy(overrides, devc_dir(self.wt) / "compose.user.yml")

    def write_prefix(self) -> None:
        """Pick the worktree's index and write it, under the lock, to the pin and to the worktree-context file."""
        nw = NetworkDomain(self.runner, self.wt, lock_dir=shared_state_dir(self.refs.main_repo_root))
        with nw.allocation_lock():
            params = nw.allocate(self.wt)
            write_pin(devc_dir(self.wt) / ".env", params)
            root_context = (self.wt / "scripts" / "dev" / "contexts" / "root-context").read_text()
            write_worktree_context(
                self.wt / ".generated" / "worktree-context",
                worktree_values(params.index, root_context),
                self.evg_name,
            )
        # Compose, op_run.sh and the kubeconfig refresh go by these two files; a local worktree has neither.
        pin = self.wt / ".generated" / ".current-evg-host"
        if self.evg_name:
            pin.parent.mkdir(exist_ok=True)
            pin.write_text(self.evg_name)
        else:
            pin.unlink(missing_ok=True)

    def switch(self, context: str) -> dict[str, str]:
        """Regenerate ``.generated/context*.env`` for ``context``; return the generated context."""
        script = str(self.wt / "scripts" / "dev" / "switch_context.sh")
        self.runner.stream([script, context], log_path=self.log("context"), cwd=self.wt, env=self.env)
        return read_env_file(self.wt / ".generated" / "context.env")

    def switch_context(self) -> bool:
        """Switch to the requested context and, when its topology is not the requested one, to the default of
        that topology. Returns whether the worktree is multi cluster."""
        generated = self.wt / ".generated"
        context = self.args.context or (generated / ".current_context").read_text().strip()
        env = self.switch(context)
        marker = generated / ".current_topology"
        recorded = marker.read_text().strip() == "multi" if marker.is_file() else None
        multi, switch_to = resolve_topology(
            self.args.multi_cluster, self.args.context, bool(env.get("MEMBER_CLUSTERS")), recorded
        )
        if switch_to:
            self.switch(switch_to)
        marker.write_text("multi\n" if multi else "single\n")
        return multi

    def recreate_clusters(self, multi: bool) -> None:
        KindDomain(self.runner).check_node_image(self.wt)
        scripts = self.wt / "scripts" / "dev"
        argv = (
            [str(scripts / "recreate_kind_clusters.sh")]
            if multi
            else [str(scripts / "recreate_kind_cluster.sh"), "kind"]
        )
        self.runner.stream(argv, log_path=self.log("kind_clusters"), cwd=self.wt, env=self.env)

    def unhealthy_clusters(self) -> bool:
        problem = KindDomain(self.runner).unhealthy(self.wt, worktree_prefix(read_pin(self.wt).index))
        if problem:
            sys.stderr.write(f"[wt-ctl] kind cluster {problem}; recreating the clusters\n")
        return problem is not None

    def prepare_host(self) -> None:
        distro, region = self.args.distro, self.args.region
        EvgDomain(self.runner).prepare_host(
            self.wt, self.evg_name, distro, region, shared_state_dir(self.refs.main_repo_root) / "evg-spawn.lock"
        )

    def recreate_on_host(self, multi: bool) -> None:
        """Sync the checkout, put the worktree's registry credentials on the host, recreate the clusters there and
        copy the kubeconfig back. ``configure`` copies the credentials of ``~/.docker`` too, so these go after it."""

        def host(*args: str) -> None:
            argv = [str(self.wt / "scripts" / "dev" / "evg_host.sh"), *args]
            self.runner.stream(argv, log_path=self.log("kind_clusters"), cwd=self.wt, env=self.env)

        if not self.args.skip_recreate:
            host("configure")
            push_to_host(self.runner, self.wt)
            if multi:
                host("cmd", "DELETE_KIND_NETWORK=false", "scripts/dev/recreate_kind_clusters.sh")
            else:
                host("cmd", "scripts/dev/recreate_kind_cluster.sh", "kind")
        host("get-kubeconfig")

    def refresh_kubeconfig(self) -> None:
        refresh_worktree(self.runner, self.wt)

    def start_devcontainer(self, devc: DevcontainerDomain) -> None:
        devc.prepare_and_up(self.log("devcontainer"))
        copy_to_container(self.runner, devc, self.wt)
        if self.evg_name:
            EvgDomain(self.runner).check_tunnel(devc)
        elif sys.platform.startswith("linux"):
            KindDomain(self.runner).join_network(self.wt, devc.project)

    def prepare_e2e(self, devc: DevcontainerDomain) -> None:
        # The reset deletes what a running operator watches, so stop it first, and wait for the apiserver:
        # the first request after `up` can arrive before the proxy is ready.
        devc.bash(
            "scripts/dev/op_run.sh --stop; n=0; until kubectl --request-timeout=3s get --raw=/readyz >/dev/null; "
            "do ((++n < 30)) || exit 1; sleep 2; done; make prepare-local-e2e",
            self.log("prepare_local_e2e"),
        )
        # prepare-local-e2e wrote multicluster.devc.kubeconfig; an Evergreen worktree needs its proxy-url.
        refresh_worktree(self.runner, self.wt)

    def start_operator(self, devc: DevcontainerDomain) -> None:
        devc.bash(
            "scripts/dev/op_run.sh --detach || echo '[op_run] the operator did not start: run op_run.sh in the container'",
            self.log("op_run"),
        )


def refresh_worktree(runner: Runner, wt: Path) -> None:
    """Rewrite the worktree's kubeconfigs for the container and register the host one with kfp; also the
    ``wt-ctl kubeconfig refresh`` verb."""
    if inside_container():
        raise WtCtlError(
            "the container cannot see the worktree's kind nodes: run `wt-ctl kubeconfig refresh` on the host"
        )
    pin = read_pin(wt)
    if pin is None:
        raise WtCtlError(f"{wt} has no worktree index: it was not created by wt-ctl")
    context = read_env_file(wt / ".generated" / "context.env").get("CLUSTER_NAME", "")
    refresh(runner, wt, worktree_prefix(pin.index), context, pin.port)
    try:
        register_worktree(wt)
    except WtCtlError as exc:
        sys.stderr.write(f"[wt-ctl] warning: kfp registration failed: {exc.render()}\n")
