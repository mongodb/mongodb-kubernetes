"""``wt-ctl`` entry point: the parser, the verbs and the one place that turns a ``WtCtlError`` into an exit code."""

from __future__ import annotations

import argparse
import signal
import sys

from .auth_cli import renew_for_attach, run_auth
from .create import Create, refresh_worktree
from .delete import delete
from .domains import kfp
from .domains.devcontainer import DevcontainerDomain
from .domains.evg import DEFAULT_DISTRO, DEFAULT_REGION
from .domains.network import NetworkDomain, env_lines_for, worktree_values
from .errors import WtCtlError
from .header import emit_banner
from .paths import WorktreeRefs, resolve_worktree, shared_state_dir, worktree_path_for
from .runner import Runner
from .status import status


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="wt-ctl",
        description="Create, inspect and delete a worktree per branch: a git worktree with a prefix of its own "
        "(devc<N>-), its kind clusters and its devcontainer. Guide: docs/dev/devcontainer-worktrees.md.",
    )
    p.add_argument("--quiet", action="store_true", help="suppress the banner.")
    sub = p.add_subparsers(dest="cmd", metavar="<verb>")

    sc = sub.add_parser(
        "create", help="create the worktree of a branch with its prefix, kind clusters and devcontainer."
    )
    sc.add_argument("branch", nargs="?", help="default: the branch of the current checkout.")
    sc.add_argument(
        "--local-kind", action="store_true", help="kind clusters on this machine (default: on an Evergreen host)."
    )
    sc.add_argument(
        "--evg-host-name", help="display name of the Evergreen host to spawn or reuse (default: the branch dir)."
    )
    sc.add_argument("--distro", default=DEFAULT_DISTRO, help="distro of a spawned host (default: %(default)s).")
    sc.add_argument("--region", default=DEFAULT_REGION, help="region of a spawned host (default: %(default)s).")
    topology = sc.add_mutually_exclusive_group()
    topology.add_argument("--single-cluster", dest="multi_cluster", action="store_false", default=None)
    topology.add_argument("--multi-cluster", dest="multi_cluster", action="store_true")
    sc.add_argument("--context", help="context to switch to (default: the checkout's current one).")
    sc.add_argument("--skip-recreate", action="store_true", help="keep the kind clusters as they are.")
    sc.add_argument(
        "--skip-evg",
        action="store_true",
        help="make no Evergreen call: no spawn, no recreate; use the host and the kubeconfig the worktree has.",
    )
    sc.add_argument("--skip-prepare-e2e", action="store_true", help="do not run make prepare-local-e2e.")

    sd = sub.add_parser("delete", help="delete targets of a worktree; nothing is deleted without a flag.")
    sd.add_argument("branch")
    sd.add_argument("--all", action="store_true", help="every target below.")
    sd.add_argument("--om", action="store_true", help="the worktree's Cloud QA Ops Manager projects.")
    sd.add_argument("--devcontainer", action="store_true", help="the compose project, its images and volumes.")
    sd.add_argument(
        "--evg", action="store_true", help="the worktree on its Evergreen host; the last one ends the host."
    )
    sd.add_argument("--clusters", action="store_true", help="the worktree's kind clusters.")
    sd.add_argument("--worktree", action="store_true", help="the git worktree (the branch stays).")
    sd.add_argument("--force", action="store_true", help="remove a worktree with uncommitted changes.")

    sub.add_parser("status", help="the worktrees of this repository.")

    sa = sub.add_parser("attach", help="a shell in this worktree's devcontainer, or `-- <command>` run in it.")
    sa.add_argument("command", nargs=argparse.REMAINDER)

    sk = sub.add_parser("kubeconfig", help="the worktree's kubeconfigs.")
    sk.add_argument("action", choices=["refresh"], help="rewrite the container's copies of the host kubeconfig.")

    sf = sub.add_parser(
        "kfp", help="the kube-forwarding-proxy daemon on a Mac host: its state and this worktree's registration."
    )
    sf.add_argument("action", nargs="?", choices=["status", "register"], default="status")

    sr = sub.add_parser("auth", help="registry credentials of this worktree: their time left, renew and roll them.")
    sr_sub = sr.add_subparsers(dest="auth_cmd", metavar="<subcommand>")
    sr_sub.add_parser("status", help="time left on each copy of the ECR token; exit 1 expiring, 3 expired or missing.")
    srf = sr_sub.add_parser("refresh", help="renew the token when under 4 hours are left and roll it to the copies.")
    srf.add_argument("--force", action="store_true", help="mint a new token even when the current one has time left.")

    sn = sub.add_parser("network", help="worktree index primitives.")
    sn_sub = sn.add_subparsers(dest="net_cmd")
    sn_al = sn_sub.add_parser("allocate", help="print what create would write for BRANCH; reserves nothing.")
    sn_al.add_argument("branch", nargs="?")
    return p


def cmd_network_allocate(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    """Print the ``.devcontainer/.env`` lines and the ``worktree-context`` lines of the next worktree."""
    nw = NetworkDomain(runner, refs.worktree_root, lock_dir=shared_state_dir(refs.main_repo_root))
    own = worktree_path_for(runner, refs, args.branch) if args.branch else refs.worktree_root
    params = nw.allocate(own)
    root_context = (refs.worktree_root / "scripts" / "dev" / "contexts" / "root-context").read_text()
    print("\n".join(env_lines_for(params)))
    print("\n".join(f'export {k}="{v}"' for k, v in worktree_values(params.index, root_context).items()))
    return 0


def cmd_kfp(refs: WorktreeRefs, args: argparse.Namespace) -> int:
    if args.action == "register":
        kfp.register_worktree(refs.worktree_root)  # does nothing on a host without the daemon
        return 0
    url = kfp.daemon_url()
    if url is None:
        print("no kfp daemon: it is a macOS service, and this host is not a Mac")
    elif not kfp.is_running(url):
        print(f"kfp not running at {url}; the launchd service starts with: {kfp.KICKSTART}")
    else:
        names = kfp.contexts(url)
        print(f"kfp running at {url}, {len(names)} registered contexts")
        print("\n".join(f"  {name}" for name in names))
    return 0


def main(argv: list[str]) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    runner = Runner()
    try:
        refs = resolve_worktree(runner)
        emit_banner(refs, quiet=args.quiet)
        if args.cmd == "create":
            args.branch = args.branch or refs.branch
            Create(runner, refs, args).run()
        elif args.cmd == "delete":
            delete(runner, refs, args)
        elif args.cmd == "status":
            status(runner, refs)
        elif args.cmd == "attach":
            command = args.command[1:] if args.command[:1] == ["--"] else args.command
            renew_for_attach(runner, refs.worktree_root)
            DevcontainerDomain(runner, refs.worktree_root).attach(command)
        elif args.cmd == "kfp":
            return cmd_kfp(refs, args)
        elif args.cmd == "auth":
            return run_auth(runner, refs, args)
        elif args.cmd == "kubeconfig":
            refresh_worktree(runner, refs.worktree_root)
        elif args.cmd == "network" and args.net_cmd == "allocate":
            return cmd_network_allocate(runner, refs, args)
        else:
            parser.print_help()
        return 0
    except WtCtlError as exc:
        sys.stderr.write(f"[wt-ctl] error: {exc.render()}\n")
        return exc.exit_code
    except KeyboardInterrupt:
        # A second SIGINT during interpreter shutdown would print an "Exception ignored" fragment.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        sys.stderr.write("[wt-ctl] interrupted\n")
        return 130
