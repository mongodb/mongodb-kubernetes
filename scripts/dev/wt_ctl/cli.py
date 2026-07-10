"""argparse-based dispatcher. The **only** module that catches ``WtCtlError``."""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path
from typing import Callable, Optional

from . import display, orchestrator_state
from .domains.compose import ComposeDomain, project_name_for
from .domains.contextsw import ContextDomain
from .domains.devcontainer import DevcontainerDomain
from .domains.evg import EvgDomain
from .domains.kfp import KFP_DEFAULT_URL, LAUNCHCTL_BOOTOUT, LAUNCHCTL_BOOTSTRAP, LAUNCHCTL_KICKSTART, KfpDomain
from .domains.kubeconfig import KubeconfigDomain
from .domains.network import NetworkDomain, gost_proxy_for, parse_devc_env, subnet_for
from .domains.omprojects import OmDomain
from .domains.worktree import WorktreeDomain
from .errors import ExternalCommandFailed, NotInWorktree, ParallelPhaseFailures, StateConflict, ToolMissing, WtCtlError
from .header import emit_banner
from .orchestrator import CreateInputs, CreateOrchestrator, DeleteInputs, DeleteOrchestrator
from .paths import WorktreeRefs, logs_dir, resolve_worktree
from .runner import Runner
from .state import GlobalStatus, KfpHostState, NetState, OrphanRegistration, WorktreeRow, WorktreeStatus

# ---------------------------------------------------------------------------
# top-level argparse
# ---------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="wt-ctl",
        description="Unified worktree + devcontainer control plane.",
    )
    p.add_argument("--quiet", action="store_true", help="suppress the banner.")
    p.add_argument(
        "--color",
        choices=("auto", "always", "never"),
        default="auto",
        help="ANSI color mode (default: auto).",
    )
    sub = p.add_subparsers(dest="cmd", metavar="<verb>")

    # high-level
    s = sub.add_parser("status", help="status of current worktree (default) or all (--all).")
    s.add_argument("branch", nargs="?")
    s.add_argument("--all", dest="all_", action="store_true")

    sc = sub.add_parser(
        "create",
        help="bring up a worktree end-to-end (worktree + EVG + devc + kubeconfig).",
    )
    sc.add_argument("branch")
    sc.add_argument("--context", help="run `make switch context=CTX` after worktree_init.")
    # Multi-cluster is the default topology; --single-cluster opts out.
    # --multi-cluster is accepted as a no-op affirmation.
    sc.add_argument(
        "--single-cluster",
        dest="single_cluster",
        action="store_true",
        help="single kind cluster instead of the default four-cluster (multi) setup.",
    )
    sc.add_argument("--multi-cluster", dest="multi_cluster", action="store_true", help=argparse.SUPPRESS)
    sc.add_argument("--skip-recreate", dest="skip_recreate", action="store_true")
    sc.add_argument("--skip-evg", dest="skip_evg", action="store_true")
    sc.add_argument("--skip-devcontainer", dest="skip_devcontainer", action="store_true")
    sc.add_argument("--skip-prepare-e2e", dest="skip_prepare_e2e", action="store_true")
    sc.add_argument("--force", action="store_true")
    sc.add_argument("--evg-host-name", dest="evg_host_name")
    sc.add_argument(
        "--distro",
        dest="distro",
        default=None,
        help="evergreen distro for the EVG host spawn (default: evg spawn's own default).",
    )
    sc.add_argument(
        "--region",
        dest="region",
        default=None,
        help="AWS region for the EVG host spawn (default: evg spawn's own default).",
    )
    sc.add_argument(
        "--local-kind",
        dest="local_kind",
        action="store_true",
        help=(
            "spin a kind cluster on the laptop instead of provisioning an "
            "EVG host. Skips EVG host create entirely; the kubeconfig "
            "lands in .generated/current.kubeconfig and KUBECONFIG points "
            "at it."
        ),
    )
    sc.add_argument(
        "--cluster-name",
        dest="cluster_name",
        help="kind cluster name in --local-kind mode (default: branch_dir).",
    )
    sc.add_argument(
        "--in-place",
        dest="in_place",
        action="store_true",
        help=(
            "operate in the current checkout instead of creating a sibling "
            "git worktree. Required in CI (EVG patch): the diff lives as "
            "uncommitted changes here, so a fresh worktree would miss it."
        ),
    )
    sc.add_argument(
        "--resume",
        action="store_true",
        help="resume a killed run from its first non-OK phase.",
    )
    sc.add_argument(
        "--restart-from",
        dest="restart_from",
        default=None,
        metavar="PHASE",
        choices=orchestrator_state.PHASE_ORDER,
        help=(
            "clear this phase and all later phases from saved state, then "
            "run from it. Phases (in order): " + ", ".join(orchestrator_state.PHASE_ORDER) + "."
        ),
    )

    sd = sub.add_parser(
        "delete",
        help=(
            "opt-in teardown: --evg / --devc / --worktree / --om "
            "(or --all). Without targets, prints help. Branches are "
            "never deleted from the git repo."
        ),
    )
    sd.add_argument("branch", nargs="?")
    # Opt-in target flags. Without any of these, the command is a no-op.
    sd.add_argument(
        "--all",
        dest="delete_all",
        action="store_true",
        help="select every target below; combine with --keep-* to opt out.",
    )
    sd.add_argument(
        "--evg",
        dest="delete_evg",
        action="store_true",
        help="terminate the EVG host pinned to this worktree.",
    )
    sd.add_argument(
        "--devc",
        dest="delete_devc",
        action="store_true",
        help="docker compose down the devcontainer stack for this worktree.",
    )
    sd.add_argument(
        "--worktree",
        dest="delete_worktree",
        action="store_true",
        help="git-remove the worktree dir + release the net-prefix entry. " "(Branches are never deleted.)",
    )
    sd.add_argument(
        "--om",
        dest="delete_om",
        action="store_true",
        help="clean cloud-qa OM projects scoped to this worktree.",
    )
    # Opt-out modifiers (only meaningful with --all).
    sd.add_argument("--keep-evg", dest="keep_evg", action="store_true")
    sd.add_argument("--keep-devc", dest="keep_devc", action="store_true")
    sd.add_argument("--keep-worktree", dest="keep_worktree", action="store_true")
    sd.add_argument("--keep-om", dest="keep_om", action="store_true")
    sd.add_argument("--evg-host-name", dest="evg_host_name")

    sub.add_parser("up", help="bring devcontainer up (idempotent).")
    sub.add_parser("down", help="stop the compose stack for this worktree.")

    sb = sub.add_parser("build", help="build the devcontainer image.")
    sb.add_argument("--no-cache", action="store_true")

    sa = sub.add_parser("attach", help="attach (no args = tmuxp; args = exec verbatim).")
    sa.add_argument("args", nargs=argparse.REMAINDER)

    slg = sub.add_parser("logs", help="tail a setup-phase log.")
    slg.add_argument("phase", nargs="?", default="build")
    slg.add_argument("-f", "--follow", action="store_true")

    # primitives
    sn = sub.add_parser("network", help="network prefix registry primitives.")
    sn_sub = sn.add_subparsers(dest="net_cmd")
    sn_sub.add_parser("list")
    sn_pr = sn_sub.add_parser("prune")
    sn_pr.add_argument("--dry-run", action="store_true")
    sn_pr.add_argument("--networks", action="store_true", help="also remove orphan docker networks (172.[16-31].x).")
    sn_rl = sn_sub.add_parser("release")
    sn_rl.add_argument("branch", nargs="?")
    sn_rl.add_argument("--dry-run", action="store_true")
    sn_al = sn_sub.add_parser("allocate")
    sn_al.add_argument("branch", nargs="?")

    se = sub.add_parser(
        "evg",
        help="EVG host primitives (status / kubeconfig / spawn / terminate / extend).",
    )
    se_sub = se.add_subparsers(dest="evg_cmd")
    se_sub.add_parser(
        "status",
        help="render the EVG host pinned to this worktree (name/id/status/ssh).",
    )
    se_sub.add_parser(
        "kubeconfig",
        help="fetch (or print cached) kubeconfig for this worktree's EVG host.",
    )
    se_sp = se_sub.add_parser(
        "spawn",
        help="spawn (or resume) an EVG host with displayName=<name>.",
    )
    se_sp.add_argument("--name", required=True, help="EVG displayName to spawn / resume.")
    se_sp.add_argument(
        "--distro",
        default="ubuntu2204-latest-large",
        help="evergreen distro (default: ubuntu2204-latest-large).",
    )
    se_sp.add_argument(
        "--region",
        default="eu-west-1",
        help="AWS region (default: eu-west-1).",
    )
    se_sp.add_argument(
        "--key",
        dest="key_name",
        help=(
            "evergreen-managed public-key name "
            "(default: $MCK_DEVC_EVG_KEY_NAME or first key from 'evergreen keys list')."
        ),
    )
    se_t = se_sub.add_parser(
        "terminate",
        help="terminate the EVG host pinned to this worktree (or --host-id).",
    )
    se_t.add_argument("--host-id")
    se_e = se_sub.add_parser(
        "extend",
        help="extend the EVG host expiration by --hours (default: 4).",
    )
    se_e.add_argument("--hours", type=int, default=4)
    se_e.add_argument("--host-id")

    sk = sub.add_parser(
        "kfp",
        help=(
            "on-host kube-forwarding-proxy daemon (pkg-installed, "
            "launchd-managed). `start`/`stop` are launchctl ops — wt-ctl "
            "only probes and PATCHes."
        ),
    )
    sk_sub = sk.add_subparsers(dest="kfp_cmd")
    sk_sub.add_parser("status")
    sk_rg = sk_sub.add_parser(
        "register",
        help="PATCH a kubeconfig to the on-host kfp daemon's /kubeconfig endpoint.",
    )
    sk_rg.add_argument(
        "--kubeconfig",
        help="kubeconfig path to register (default: <worktree>/.generated/current.kubeconfig).",
    )
    sk_rg.add_argument(
        "--replace",
        action="store_true",
        help=(
            "HTTP PUT instead of PATCH — WIPES every sibling worktree's "
            "registration. Default PATCH already overwrites same-name "
            "entries idempotently; prefer the default."
        ),
    )
    sk_rg.add_argument(
        "--url",
        default=None,
        help=(
            "Target kfp base URL (default: http://127.0.0.1:11616). Use "
            "http://host.docker.internal:11616 to register from inside a "
            "devcontainer."
        ),
    )
    sk_sub.add_parser(
        "reset",
        help="HTTP DELETE the daemon's dynamic kubeconfig (wipes all registrations).",
    )

    so = sub.add_parser("om", help="cloud-qa OM project primitives.")
    so_sub = so.add_subparsers(dest="om_cmd")
    so_sub.add_parser("list")
    so_sub.add_parser("clean")

    sc = sub.add_parser("ctx", help="context primitives.")
    sc_sub = sc.add_subparsers(dest="ctx_cmd")
    sc_sub.add_parser("show")
    sc_sw = sc_sub.add_parser("switch")
    sc_sw.add_argument("ctx")

    skc = sub.add_parser(
        "kubeconfig",
        help="post-process .generated/current.kubeconfig per side + register with kfp.",
    )
    skc_sub = skc.add_subparsers(dest="kubeconfig_cmd")
    skc_sub.add_parser(
        "refresh",
        help=(
            "Read .generated/current.kubeconfig, write .generated/current.devc.kubeconfig, "
            "and PATCH the appropriate variant to K8S_FWD_PROXY/kubeconfig. "
            "Dispatch: .current-evg-host present → proxy-url patch; absent + "
            "loopback server → host.docker.internal rewrite; else → identity "
            "copy (BYOC)."
        ),
    )

    sw = sub.add_parser("wt", help="git-worktree primitives.")
    sw_sub = sw.add_subparsers(dest="wt_cmd")
    sw_sub.add_parser("list")
    sw_n = sw_sub.add_parser("new")
    sw_n.add_argument("branch")
    sw_n.add_argument("-f", "--force", action="store_true")
    sw_r = sw_sub.add_parser("rm")
    sw_r.add_argument("branch", nargs="?")
    sw_r.add_argument("-f", "--force", action="store_true")

    return p


# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------


def main(argv: list[str]) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    runner = Runner()

    # `status --all`, `create <branch>`, and `delete <branch>` are allowed
    # outside any worktree (they take an explicit target). Everything else
    # resolves the cwd worktree first.
    cmd = args.cmd
    skip_wt = (
        (cmd == "status" and getattr(args, "all_", False))
        or (cmd == "status" and getattr(args, "branch", None))
        or cmd == "create"
        or (cmd == "delete" and getattr(args, "branch", None))
        or cmd == "kfp"
    )
    refs: Optional[WorktreeRefs] = None
    try:
        if not skip_wt:
            refs = resolve_worktree(runner)
        else:
            # Best-effort resolution: still useful for `delete` with no
            # branch (default to current worktree's branch).
            try:
                refs = resolve_worktree(runner)
            except WtCtlError:
                refs = None
        # ``status <branch>`` retargets refs (so the banner + body both
        # describe the requested worktree, not the cwd).
        if cmd == "status" and getattr(args, "branch", None) and not getattr(args, "all_", False):
            target_refs = _refs_for_branch(runner, refs, args.branch)
            if target_refs is None:
                emit_banner(refs, quiet=args.quiet)
                sys.stderr.write(f"[wt-ctl] error: no worktree for branch / dir " f"'{args.branch}'\n")
                return 2
            refs = target_refs
        emit_banner(refs, quiet=args.quiet)
        return _dispatch(args, runner, refs)
    except WtCtlError as exc:
        sys.stderr.write(f"[wt-ctl] error: {exc.render()}\n")
        return getattr(exc, "exit_code", 1)


def _dispatch(args: argparse.Namespace, runner: Runner, refs: Optional[WorktreeRefs]) -> int:
    cmd = args.cmd
    if cmd is None:
        build_parser().print_help()
        return 0
    if cmd == "status":
        if args.all_:
            return cmd_status_all(runner, args)
        assert refs is not None
        return cmd_status(runner, refs, args)
    if cmd == "create":
        return cmd_create(runner, refs, args)
    if cmd == "delete":
        return cmd_delete(runner, refs, args)
    if cmd == "up":
        assert refs is not None
        DevcontainerDomain(runner).up(refs.worktree_root)
        return 0
    if cmd == "down":
        assert refs is not None
        ComposeDomain(runner).down(refs.worktree_root)
        return 0
    if cmd == "build":
        assert refs is not None
        DevcontainerDomain(runner).build(refs.worktree_root, no_cache=args.no_cache)
        return 0
    if cmd == "attach":
        assert refs is not None
        # argparse REMAINDER may include a leading "--"; strip it.
        attach_args = list(args.args or [])
        if attach_args and attach_args[0] == "--":
            attach_args = attach_args[1:]
        DevcontainerDomain(runner).attach(refs.worktree_root, attach_args)
        return 0  # never reached (exec)
    if cmd == "logs":
        assert refs is not None
        return cmd_logs(refs, args)
    if cmd == "network":
        assert refs is not None
        return cmd_network(runner, refs, args)
    if cmd == "evg":
        assert refs is not None
        return cmd_evg(runner, refs, args)
    if cmd == "kfp":
        return cmd_kfp(runner, args)
    if cmd == "om":
        assert refs is not None
        return cmd_om(runner, refs, args)
    if cmd == "ctx":
        assert refs is not None
        return cmd_ctx(runner, refs, args)
    if cmd == "kubeconfig":
        assert refs is not None
        return cmd_kubeconfig(runner, refs, args)
    if cmd == "wt":
        assert refs is not None
        return cmd_wt(runner, refs, args)
    sys.stderr.write(f"[wt-ctl] error: unknown subcommand: {cmd}\n")
    return 2


# ---------------------------------------------------------------------------
# status
# ---------------------------------------------------------------------------


def cmd_status(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    wt_dom = WorktreeDomain(runner)
    cp_dom = ComposeDomain(runner)
    # Workhorse scripts live in each worktree (not the main repo's .git
    # parent); pass the worktree root as their "repo_root".
    nw_dom = NetworkDomain(runner, refs.worktree_root)
    ev_dom = EvgDomain(runner, refs.worktree_root)
    om_dom = OmDomain(runner, refs.worktree_root)
    kc_dom = KubeconfigDomain(runner)
    ctx_dom = ContextDomain(runner)

    git = wt_dom.git_state(refs.worktree_root)
    devc = cp_dom.project_state(refs.worktree_root)

    devc_env = parse_devc_env(refs.worktree_root / ".devcontainer")
    prefix_str = devc_env.get("MCK_DEVC_NET_PREFIX")
    prefix: Optional[int] = None
    if prefix_str and prefix_str.isdigit():
        prefix = int(prefix_str)

    entries = []
    try:
        entries = nw_dom.list_entries()
    except ExternalCommandFailed:
        pass
    registered = any(e.branch_dir == refs.branch_dir for e in entries)
    if prefix is None:
        for e in entries:
            if e.branch_dir == refs.branch_dir:
                prefix = e.prefix
                break
    orphans = sum(1 for e in entries if e.status == "stale")

    # Read NAMESPACE from rendered context.env if present (definitive).
    ctx_env = _read_context_env(refs.worktree_root)
    namespace = ctx_env.get("NAMESPACE")
    if not namespace and prefix is not None:
        namespace = f"<NAMESPACE>-{prefix}"  # show the suffix pattern even pre-switch

    net = NetState(
        prefix=prefix,
        subnet=subnet_for(prefix) if prefix is not None else None,
        namespace=namespace,
        gost_proxy=gost_proxy_for(prefix) if prefix is not None else None,
        registered=registered,
        orphans=orphans,
    )

    # EVG host (best-effort; tolerate evergreen CLI being missing).
    evg = None
    try:
        evg = ev_dom.state_for(refs.worktree_root)
    except ToolMissing:
        evg = None

    kc = kc_dom.state_for(refs.worktree_root)
    om = om_dom.state_for(refs.worktree_root)
    ctx = ctx_dom.current(refs.worktree_root)

    kfp_dom = KfpDomain(runner)
    kfp_st = kfp_dom.status()
    kfp_state = KfpHostState(
        listening=kfp_st.listening,
        health=kfp_st.health,
        http_endpoint=kfp_st.http_endpoint,
    )

    next_hints = []
    if devc and devc.state == "running":
        next_hints.append("wt-ctl attach          # tmuxp mck")
        next_hints.append("wt-ctl attach zsh      # bare shell")
    elif devc and devc.state == "exited":
        next_hints.append("wt-ctl up              # bring stack back online")
    else:
        next_hints.append("wt-ctl up              # boot devcontainer")

    # EVG host expiry warning: surface an extend hint when < 8h left so the
    # user can act before the host reaps mid-task.
    if evg is not None and evg.expires_seconds is not None and evg.expires_seconds < 8 * 3600:
        next_hints.append(f"wt-ctl evg extend      # EVG host expires in {evg.expires_in}")

    snap = WorktreeStatus(
        worktree_dir=refs.branch_dir,
        worktree_path=str(refs.worktree_root),
        git=git,
        context=ctx,
        devc=devc,
        network=net,
        evg=evg,
        kubeconfig=kc,
        om=om,
        kfp=kfp_state,
        next_hints=next_hints,
    )
    rendered = display.render_status(snap, color=args.color)
    extra = _orchestrator_status_line(refs.worktree_root, refs.branch)
    if extra:
        rendered = rendered + "\n\n" + extra
    print(rendered)
    return 0


def cmd_status_all(runner: Runner, args: argparse.Namespace) -> int:
    """Repo-wide view: joins `git worktree list` with network registry + EVG data."""
    # Resolved repo root, not cwd: NetworkDomain and EvgDomain anchor on it, and
    # running from a subdirectory would otherwise mislabel live stacks.
    main_repo = _resolve_main_repo(runner)
    wt_dom = WorktreeDomain(runner)
    cp_dom = ComposeDomain(runner)
    nw_dom = NetworkDomain(runner, main_repo)
    ev_dom = EvgDomain(runner, main_repo)

    wts = wt_dom.list_worktrees(main_repo)
    try:
        net_entries = nw_dom.list_entries()
    except ExternalCommandFailed:
        net_entries = []
    by_branch_dir = {e.branch_dir: e for e in net_entries}

    try:
        hosts = ev_dom.list_hosts(mine=True)
    except ToolMissing:
        hosts = []
    hosts_by_name = {h.get("name"): h for h in hosts}

    rows: list[WorktreeRow] = []
    seen_branch_dirs: set[str] = set()
    for wt in wts:
        branch_dir = wt.path.name
        seen_branch_dirs.add(branch_dir)
        branch = wt.branch or "(detached)"
        net = by_branch_dir.get(branch_dir)
        prefix = net.prefix if net else None

        devc_state = "-"
        devc = cp_dom.project_state(wt.path)
        if devc:
            devc_state = devc.state
        # Surface "incomplete" when the orchestrator state.json shows any
        # non-OK phase. We render this as devc_state because the column is
        # already there and a stalled create is exactly a partial devc.
        try:
            wt_state = orchestrator_state.load(wt.path)
        except WtCtlError:
            wt_state = None
        if wt_state is not None and wt_state.first_non_ok() is not None:
            devc_state = "incomplete" if devc_state in ("-", "exited", "running") else devc_state

        # By convention the EVG host display name == branch_dir.
        host = hosts_by_name.get(branch_dir)
        evg_name = host.get("name") if host else None
        evg_status = host.get("status") if host else None

        ns = f"mck-devc-{prefix}-mongodb-test" if prefix is not None else None

        prunable = False
        reason: Optional[str] = None
        delete_cmd: Optional[str] = None
        if wt.prunable:
            prunable, reason = True, "worktree-prunable"
        elif devc and devc.state == "exited" and not (host and host.get("status") == "running"):
            prunable, reason = True, "devc-exited"
        elif evg_status in ("terminating", "terminated", "decommissioned"):
            prunable, reason = True, f"evg-{evg_status}"
        if prunable:
            target_branch = wt.branch or branch_dir
            delete_cmd = f"wt-ctl delete {target_branch}"

        rows.append(
            WorktreeRow(
                worktree=branch_dir,
                branch=branch,
                prefix=prefix,
                namespace=ns,
                devc_state=devc_state,
                evg_name=evg_name,
                evg_status=evg_status,
                evg_expires=None,
                prunable=prunable,
                prunable_reason=reason,
                delete_cmd=delete_cmd,
            )
        )

    orphans: list[OrphanRegistration] = []
    for entry in net_entries:
        if entry.branch_dir in seen_branch_dirs:
            continue
        # Registered but no matching worktree on this side → orphan.
        if entry.status in ("active", "stale"):
            orphans.append(
                OrphanRegistration(
                    branch_dir=entry.branch_dir,
                    prefix=entry.prefix,
                    release_cmd=f"wt-ctl network release {entry.branch_dir}",
                )
            )

    g = GlobalStatus(rows=rows, orphans=orphans)
    print(display.render_status_all(g, color=args.color))
    return 0


def _resolve_main_repo(runner: Runner) -> Path:
    """Best-effort: walk up to find a `.git`, then ask git for the common dir
    (which lives in the main repo). Falls back to cwd if outside.
    """
    cwd = Path.cwd().resolve()
    cur = cwd
    while True:
        if (cur / ".git").exists():
            break
        if cur.parent == cur:
            return cwd
        cur = cur.parent
    res = runner.run(
        ["git", "-C", str(cur), "rev-parse", "--git-common-dir"],
        check=False,
    )
    if res.rc != 0:
        return cur
    common = (cur / res.stdout.strip()).resolve()
    return common.parent


# ---------------------------------------------------------------------------
# logs
# ---------------------------------------------------------------------------


def cmd_logs(refs: WorktreeRefs, args: argparse.Namespace) -> int:
    phase = args.phase or "build"
    log_path = logs_dir(refs.worktree_root) / f"{phase}.log"
    if not log_path.is_file():
        # Also accept the `dc_<phase>.log` name.
        alt = logs_dir(refs.worktree_root) / f"dc_{phase}.log"
        if alt.is_file():
            log_path = alt
        else:
            sys.stderr.write(f"[wt-ctl] no log at {log_path} (and no dc_{phase}.log)\n")
            return 0
    if args.follow:
        # Use exec_replace so Ctrl-C reaches `tail` directly.
        Runner().exec_replace(["tail", "-n", "200", "-f", str(log_path)])
        return 0
    print(log_path.read_text(), end="")
    return 0


# ---------------------------------------------------------------------------
# network primitives
# ---------------------------------------------------------------------------


def cmd_network(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    nw = NetworkDomain(runner, refs.worktree_root)
    sub = args.net_cmd or "list"
    if sub == "list":
        sys.stdout.write(nw.list_raw())
        return 0
    if sub == "prune":
        sys.stdout.write(
            nw.prune(
                dry_run=args.dry_run,
                prune_networks=getattr(args, "networks", False),
            )
        )
        return 0
    if sub == "release":
        target = args.branch or refs.branch_dir
        sys.stdout.write(nw.release(target, dry_run=getattr(args, "dry_run", False)))
        return 0
    if sub == "allocate":
        target = args.branch or refs.branch_dir
        sys.stdout.write(nw.allocate(target))
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown network subcommand: {sub}\n")
    return 2


# ---------------------------------------------------------------------------
# evg primitives
# ---------------------------------------------------------------------------


def cmd_evg(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    ev = EvgDomain(runner, refs.worktree_root)
    sub = args.evg_cmd or "status"
    if sub == "status":
        st = ev.state_for(refs.worktree_root)
        if st is None:
            print("(no EVG host pinned)")
            return 0
        print(display.render_kv(_evg_state_pairs(st)))
        return 0
    if sub == "kubeconfig":
        sys.stdout.write(ev.get_kubeconfig(refs.worktree_root))
        return 0
    if sub == "terminate":
        host_id = args.host_id or _resolve_host_id(ev, refs, mine_only=True)
        if not host_id:
            sys.stderr.write("[wt-ctl] error: no host id (pass --host-id)\n")
            return 2
        sys.stdout.write(ev.terminate(host_id))
        return 0
    if sub == "extend":
        host_id = args.host_id or _resolve_host_id(ev, refs, mine_only=True)
        if not host_id:
            sys.stderr.write("[wt-ctl] error: no host id (pass --host-id)\n")
            return 2
        sys.stdout.write(ev.extend(host_id, args.hours))
        return 0
    if sub == "spawn":
        host_id = ev.spawn(
            name=args.name,
            distro=args.distro,
            region=args.region,
            key_name=args.key_name,
            emit=lambda m: sys.stderr.write(m + "\n"),
        )
        print(host_id)
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown evg subcommand: {sub}\n")
    return 2


def _resolve_host_id(ev: EvgDomain, refs: WorktreeRefs, *, mine_only: bool = False) -> Optional[str]:
    st = ev.state_for(refs.worktree_root, mine_only=mine_only)
    return st.id if st else None


def _evg_state_pairs(st):
    pairs = [("name", st.name or "-"), ("id", st.id or "-"), ("status", st.status or "-")]
    if st.host_name:
        pairs.append(("host", st.host_name))
    if st.ssh:
        pairs.append(("ssh", st.ssh))
    return pairs


# ---------------------------------------------------------------------------
# kfp primitives
# ---------------------------------------------------------------------------


def cmd_kfp(runner: Runner, args: argparse.Namespace) -> int:
    kfp = KfpDomain(runner)
    sub = args.kfp_cmd or "status"
    if sub == "status":
        st = kfp.status()
        if st.listening:
            health = st.health or "unreachable"
            print(f"running    http={st.http_endpoint}    health={health}")
            return 0
        print("not_running    (pkg-installed daemon; restart with: " f"{LAUNCHCTL_KICKSTART})")
        return 0
    if sub == "register":
        explicit = getattr(args, "kubeconfig", None)
        replace = getattr(args, "replace", False)
        url = getattr(args, "url", None) or KFP_DEFAULT_URL
        if explicit:
            kc_path = Path(explicit).expanduser()
        else:
            # Default: current worktree's host-side kubeconfig.
            try:
                wt = resolve_worktree(runner, Path.cwd())
            except NotInWorktree:
                sys.stderr.write(
                    "[wt-ctl] kfp register: not inside a worktree; " "pass --kubeconfig PATH explicitly.\n"
                )
                return 2
            kc_path = wt.worktree_root / ".generated" / "current.kubeconfig"
        body = kfp.register(kc_path, replace=replace, target_url=url)
        verb = "replaced" if replace else "registered"
        print(f"{verb}    kubeconfig={kc_path}    target={url}")
        if body.strip():
            print(f"  daemon response: {body.strip()}")
        return 0
    if sub == "reset":
        kfp.reset()
        print("reset    (kfp dynamic kubeconfig wiped)")
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown kfp subcommand: {sub}\n")
    return 2


# ---------------------------------------------------------------------------
# om primitives
# ---------------------------------------------------------------------------


def cmd_om(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    om = OmDomain(runner, refs.worktree_root)
    sub = args.om_cmd or "list"
    if sub == "list":
        projects = om.list_projects(refs.worktree_root)
        if not projects:
            print("(no projects in scope)")
            return 0
        for p in projects:
            print(f"{p.get('id', '-'):24}  {p.get('name', '-')}")
        return 0
    if sub == "clean":
        om.clean(refs.worktree_root)
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown om subcommand: {sub}\n")
    return 2


# ---------------------------------------------------------------------------
# ctx primitives
# ---------------------------------------------------------------------------


def cmd_kubeconfig(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    sub = args.kubeconfig_cmd or "refresh"
    if sub == "refresh":
        # Merge the freshly-generated context env files on top of os.environ
        # so callers who invoked us right after `make switch` see the new
        # CLUSTER_NAME / MCK_DEVC_PROXY_PORT / EVG_HOST_PROXY / K8S_FWD_PROXY
        # values without needing to re-source devenv first.
        env = dict(os.environ)
        for name in ("context.env", "context.host.env", "context.devc.env"):
            p = refs.worktree_root / ".generated" / name
            if not p.is_file():
                continue
            for raw in p.read_text().splitlines():
                line = raw.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                v = v.strip()
                if (v.startswith('"') and v.endswith('"')) or (v.startswith("'") and v.endswith("'")):
                    v = v[1:-1]
                env[k.strip()] = v
        KubeconfigDomain(runner).refresh(refs.worktree_root, env=env)
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown kubeconfig subcommand: {sub}\n")
    return 2


def cmd_ctx(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    cx = ContextDomain(runner)
    sub = args.ctx_cmd or "show"
    if sub == "show":
        cur = cx.current(refs.worktree_root)
        if not cur:
            print("(no context selected)")
            return 0
        print(cur)
        return 0
    if sub == "switch":
        cx.switch(refs.worktree_root, args.ctx)
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown ctx subcommand: {sub}\n")
    return 2


# ---------------------------------------------------------------------------
# wt primitives
# ---------------------------------------------------------------------------


def cmd_wt(runner: Runner, refs: WorktreeRefs, args: argparse.Namespace) -> int:
    wd = WorktreeDomain(runner)
    sub = args.wt_cmd or "list"
    if sub == "list":
        for wt in wd.list_worktrees(refs.main_repo_root):
            tags = []
            if wt.is_main:
                tags.append("main")
            if wt.detached:
                tags.append("detached")
            if wt.prunable:
                tags.append("prunable")
            tag_str = f" [{', '.join(tags)}]" if tags else ""
            branch = wt.branch or "(detached)"
            print(f"{branch:60} {wt.path}{tag_str}")
        return 0
    if sub == "new":
        wd.add(refs.main_repo_root, args.branch, force=args.force)
        return 0
    if sub == "rm":
        target = (
            refs.worktree_root if not args.branch else _find_worktree_by_branch(wd, refs.main_repo_root, args.branch)
        )
        if target is None:
            sys.stderr.write(f"[wt-ctl] error: no worktree for branch '{args.branch}'\n")
            return 2
        wd.remove(refs.main_repo_root, target, force=args.force)
        return 0
    sys.stderr.write(f"[wt-ctl] error: unknown wt subcommand: {sub}\n")
    return 2


def _find_worktree_by_branch(wd: WorktreeDomain, repo: Path, branch: str) -> Optional[Path]:
    for wt in wd.list_worktrees(repo):
        if wt.branch == branch:
            return wt.path
    return None


def _refs_for_branch(
    runner: Runner,
    cwd_refs: Optional[WorktreeRefs],
    target: str,
) -> Optional[WorktreeRefs]:
    """Resolve ``WorktreeRefs`` for an explicit branch (or directory basename).

    Used by ``wt-ctl status <branch>``: lets the user inspect another
    worktree without ``cd``-ing into it. Matches by exact branch name
    first, then by directory basename (so ``lsierant_KUBE-17-…`` and
    ``lsierant/KUBE-17-…`` both work).
    """
    main_repo = cwd_refs.main_repo_root if cwd_refs is not None else _resolve_main_repo(runner)
    wd = WorktreeDomain(runner)
    target_path: Optional[Path] = _find_worktree_by_branch(wd, main_repo, target)
    if target_path is None:
        for wt in wd.list_worktrees(main_repo):
            if wt.path.name == target:
                target_path = wt.path
                break
    if target_path is None:
        return None
    try:
        return resolve_worktree(runner, cwd=target_path)
    except WtCtlError:
        return None


# ---------------------------------------------------------------------------
# create / delete
# ---------------------------------------------------------------------------


def cmd_create(runner: Runner, refs: Optional[WorktreeRefs], args: argparse.Namespace) -> int:
    branch: str = args.branch
    branch_dir = branch.replace("/", "_")
    main_repo, worktree_path, host_worktree = _resolve_create_paths(runner, refs, branch_dir)
    # In-place: operate in the invoking checkout (which holds the CI patch diff)
    # rather than a sibling worktree that would only carry committed history.
    # host_worktree is already the invoking checkout (refs.worktree_root, or
    # main_repo when run outside any worktree); target that, not the primary
    # checkout — the two differ when invoked from a linked worktree.
    if getattr(args, "in_place", False):
        worktree_path = host_worktree
    inputs = CreateInputs(
        branch=branch,
        branch_dir=branch_dir,
        worktree_path=worktree_path,
        main_repo_root=main_repo,
        host_worktree_root=host_worktree,
        context=args.context,
        multi_cluster=not args.single_cluster,
        skip_recreate=args.skip_recreate,
        skip_evg=args.skip_evg,
        skip_devcontainer=args.skip_devcontainer,
        skip_prepare_e2e=args.skip_prepare_e2e,
        force=args.force,
        evg_host_name=args.evg_host_name,
        distro=getattr(args, "distro", None),
        region=getattr(args, "region", None),
        local_kind=getattr(args, "local_kind", False),
        cluster_name=getattr(args, "cluster_name", None),
        in_place=getattr(args, "in_place", False),
    )
    # On resume/restart, reload behavioural flags from the prior run's state.json
    # (see CreateInputs.with_persisted_flags); paths stay freshly resolved.
    if args.resume or args.restart_from:
        prior = orchestrator_state.load(worktree_path)
        if prior is not None and prior.inputs:
            inputs = inputs.with_persisted_flags(prior.inputs)
    _ensure_host_kfp(runner, args.skip_evg)

    orch = CreateOrchestrator(runner, inputs)
    try:
        orch.run(
            resume=args.resume,
            restart_from=args.restart_from,
            emit=lambda msg: sys.stderr.write(msg + "\n"),
        )
    except WtCtlError as exc:
        # Record the failing phase + resume hint for the user.
        sys.stderr.write(
            f"[wt-ctl] error: {exc.render()}\n" f"[wt-ctl] resume with: scripts/dev/wt-ctl create {branch} --resume\n"
        )
        return getattr(exc, "exit_code", 1)
    return 0


_DELETE_HELP = """\
[wt-ctl] delete: no targets selected — nothing was deleted.

Select what to tear down. Branches are NEVER deleted from the git repo.

  --evg          terminate the EVG host
  --devc         compose down the devcontainer stack
  --worktree     remove the worktree dir + release the net-prefix entry
  --om           clean cloud-qa OM projects for this worktree
  --all          all of the above
                 (combine with --keep-* to opt out of one target)

Examples:
  wt-ctl delete --evg --devc            # release cloud resources, keep worktree
  wt-ctl delete --all                   # tear everything down
  wt-ctl delete --all --keep-worktree   # everything but keep the worktree dir
  wt-ctl delete --worktree --om         # local-only cleanup
"""


def cmd_delete(runner: Runner, refs: Optional[WorktreeRefs], args: argparse.Namespace) -> int:
    # Default to the current worktree's branch when no positional arg was given.
    branch: Optional[str] = args.branch
    if not branch:
        if refs is None:
            sys.stderr.write("[wt-ctl] error: no branch given and not inside a worktree.\n")
            return 2
        branch = refs.branch
    if not branch or branch == "HEAD":
        sys.stderr.write("[wt-ctl] error: refusing to delete a detached HEAD\n")
        return 2

    # Resolve target set: --all expands to everything, then --keep-* opts out.
    delete_evg = args.delete_evg or args.delete_all
    delete_devc = args.delete_devc or args.delete_all
    delete_worktree = args.delete_worktree or args.delete_all
    delete_om = args.delete_om or args.delete_all
    if args.keep_evg:
        delete_evg = False
    if args.keep_devc:
        delete_devc = False
    if args.keep_worktree:
        delete_worktree = False
    if args.keep_om:
        delete_om = False

    if not (delete_evg or delete_devc or delete_worktree or delete_om):
        sys.stderr.write(_DELETE_HELP)
        return 0

    branch_dir = branch.replace("/", "_")
    main_repo, worktree_path, _host = _resolve_create_paths(runner, refs, branch_dir)

    inputs = DeleteInputs(
        branch=branch,
        branch_dir=branch_dir,
        worktree_path=worktree_path,
        main_repo_root=main_repo,
        delete_om=delete_om,
        delete_devc=delete_devc,
        delete_evg=delete_evg,
        delete_worktree=delete_worktree,
        evg_host_name=args.evg_host_name,
    )
    orch = DeleteOrchestrator(runner, inputs)
    orch.run(emit=lambda msg: sys.stderr.write(msg + "\n"))
    return 0


def _ensure_host_kfp(runner: Runner, skip_evg: bool) -> None:
    """Probe that the on-host kfp daemon is listening on 127.0.0.1:11616. The
    launchd-managed daemon isn't started here; if it's down, warn and continue
    (in-pipeline PATCHes handle a missing daemon best-effort)."""
    if skip_evg:
        sys.stderr.write("[wt-ctl] kfp pre-flight: skipped (--skip-evg)\n")
        return
    if KfpDomain(runner).is_listening():
        sys.stderr.write("[wt-ctl] kfp pre-flight: daemon reachable on 127.0.0.1:11616\n")
        return
    sys.stderr.write(
        "[wt-ctl] kfp pre-flight: WARN daemon not listening on 127.0.0.1:11616. "
        "Daemon is launchd-managed; bring it up with:\n"
        f"           {LAUNCHCTL_KICKSTART}\n"
        "         (or if the pkg was never installed: see kube-forwarding-proxy "
        "packaging/macos/build.sh). Continuing best-effort.\n"
    )


def _resolve_create_paths(
    runner: Runner,
    refs: Optional[WorktreeRefs],
    branch_dir: str,
) -> tuple[Path, Path, Path]:
    """Compute (main_repo_root, target_worktree_path, host_worktree_root).

    The convention (see ``wt_setup.sh``): the new worktree is a sibling of
    the calling worktree. When the user invokes wt-ctl from outside any
    worktree, we fall back to the main repo's parent for the sibling slot.
    """
    if refs is not None:
        anchor = refs.worktree_root
        target = (anchor.parent / branch_dir).resolve()
        return refs.main_repo_root, target, refs.worktree_root
    main_repo = _resolve_main_repo(runner)
    target = (main_repo.parent / branch_dir).resolve()
    return main_repo, target, main_repo


# ---------------------------------------------------------------------------
# status integration with orchestrator state
# ---------------------------------------------------------------------------


def _orchestrator_status_line(worktree_root: Path, branch: str) -> Optional[str]:
    """If ``.generated/wt-ctl/state.json`` exists and any phase is non-OK,
    return a one-liner the status renderer can append; otherwise None.
    """
    try:
        st = orchestrator_state.load(worktree_root)
    except WtCtlError:
        return None
    if st is None:
        return None
    failed = next(
        (
            name
            for name, rec in st.phases.items()
            if rec.status in (orchestrator_state.FAILED, orchestrator_state.IN_PROGRESS)
        ),
        None,
    )
    if not failed:
        # Find first non-OK to surface "incomplete" state.
        non_ok = next(
            (
                name
                for name in orchestrator_state.PHASE_ORDER
                if (rec := st.phases.get(name)) is None
                or rec.status not in (orchestrator_state.OK, orchestrator_state.SKIPPED)
            ),
            None,
        )
        if non_ok is None:
            return None
        return f"last_create  incomplete at {non_ok}  " f"(resume: wt-ctl create {branch} --resume)"
    return f"last_create  failed at {failed}  " f"(resume: wt-ctl create {branch} --resume)"


def _read_context_env(worktree_root: Path) -> dict[str, str]:
    """Read context.env into a dict so ``status`` can read NAMESPACE without
    instantiating OmDomain."""
    out: dict[str, str] = {}
    f = worktree_root / ".generated" / "context.env"
    if not f.is_file():
        return out
    for raw in f.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        v = v.strip()
        if (v.startswith('"') and v.endswith('"')) or (v.startswith("'") and v.endswith("'")):
            v = v[1:-1]
        out[k.strip()] = v
    return out
