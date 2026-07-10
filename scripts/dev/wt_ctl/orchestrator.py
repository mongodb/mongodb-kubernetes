"""``wt-ctl create`` and ``wt-ctl delete`` orchestrators.

``create`` runs a data-driven phase pipeline with per-phase state tracking and
resume; ``delete`` is a forward-only best-effort teardown. Both take a Runner
and a worktree path; neither imports subprocess directly.

A ``Phase`` carries its name, action, and input-hash function; the runner
iterates the phase tuple and decides skip / restart / fail / record in one
place. The ``evg_prepare`` + ``dc_build`` pair is detected by name and run
concurrently when both are due, recording each individually. Delete steps are
best-effort (logged and continued on failure) with no state file.
"""

from __future__ import annotations

import os
import platform
import sys
import time
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import Callable, Iterable, Optional

from . import orchestrator_state as ostate
from .domains.compose import project_name_for
from .domains.network import DERIVED_ENV_KEYS, NetworkDomain, env_lines_for, stack_params
from .errors import ExternalCommandFailed, ParallelPhaseFailures, StateConflict, ToolMissing, WtCtlError
from .paths import logs_dir
from .runner import Runner

# ---------------------------------------------------------------------------
# shared helpers
# ---------------------------------------------------------------------------


def _load_context_env(wt: Path) -> dict[str, str]:
    """Parse .generated/context*.env (plain, possibly-quoted KEY=value) into a
    dict so it can be passed to in-process KubeconfigDomain.refresh without
    pre-loading the orchestrator's shell env."""
    out: dict[str, str] = {}
    for name in ("context.env", "context.host.env"):
        path = wt / ".generated" / name
        if not path.is_file():
            continue
        for raw in path.read_text().splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            v = v.strip()
            if (v.startswith('"') and v.endswith('"')) or (v.startswith("'") and v.endswith("'")):
                v = v[1:-1]
            out[k.strip()] = v
    return out


def _devc_bash(wt: Path, script: str) -> list[str]:
    """``devcontainer exec ... bash -lc <script>`` argv — a non-interactive
    login shell in the worktree's devcontainer. ``script`` is the inner
    command, typically ``set -Eeou pipefail; cd /workspace; . scripts/dev/devenv; <action>``."""
    return [
        "devcontainer",
        "exec",
        "--workspace-folder",
        str(wt),
        "bash",
        "-lc",
        script,
    ]


# ---------------------------------------------------------------------------
# inputs the user passes to `wt-ctl create`
# ---------------------------------------------------------------------------


@dataclass
class CreateInputs:
    """Flags passed to ``wt-ctl create``. Stored verbatim in state.json so
    resume can detect changed inputs and invalidate hashes."""

    branch: str
    branch_dir: str
    worktree_path: Path  # target worktree root (may not yet exist)
    main_repo_root: Path  # the main repo (parent of .git/common dir)
    # The worktree the user invoked from; PROJECT_DIR for create_worktree.sh so
    # the new worktree is its sibling. Falls back to main_repo_root outside any worktree.
    host_worktree_root: Path
    context: Optional[str] = None
    # CLI default is multi-cluster (--single-cluster opts out); False here is
    # only the unset baseline for programmatic/test construction.
    multi_cluster: bool = False
    skip_recreate: bool = False
    skip_evg: bool = False
    skip_devcontainer: bool = False
    skip_prepare_e2e: bool = False
    force: bool = False
    evg_host_name: Optional[str] = None  # default: branch_dir
    # EVG spawn overrides; None → evg spawn's defaults (ubuntu2204-latest-large / eu-west-1).
    distro: Optional[str] = None
    region: Optional[str] = None
    # Local-kind: kind cluster on this laptop instead of an EVG host; cluster_name defaults to branch_dir.
    local_kind: bool = False
    cluster_name: Optional[str] = None
    # In-place: operate in the current checkout (worktree_path == main_repo_root)
    # instead of creating a worktree. Used in CI where the patch diff is uncommitted.
    in_place: bool = False

    def resolved_evg_host_name(self) -> str:
        return self.evg_host_name or self.branch_dir

    def resolved_cluster_name(self) -> str:
        # branch_dir keeps parallel worktrees off the same kind cluster.
        return self.cluster_name or self.branch_dir

    def to_dict(self) -> dict:
        return {
            "branch": self.branch,
            "branch_dir": self.branch_dir,
            "worktree_path": str(self.worktree_path),
            "main_repo_root": str(self.main_repo_root),
            "host_worktree_root": str(self.host_worktree_root),
            "context": self.context,
            "multi_cluster": self.multi_cluster,
            "skip_recreate": self.skip_recreate,
            "skip_evg": self.skip_evg,
            "skip_devcontainer": self.skip_devcontainer,
            "skip_prepare_e2e": self.skip_prepare_e2e,
            "force": self.force,
            "evg_host_name": self.resolved_evg_host_name(),
            "distro": self.distro,
            "region": self.region,
            "local_kind": self.local_kind,
            "cluster_name": self.resolved_cluster_name() if self.local_kind else None,
            "in_place": self.in_place,
        }

    def with_persisted_flags(self, saved: dict) -> "CreateInputs":
        """Copy with behavioural flags reloaded from a prior run's persisted
        ``inputs`` but paths kept fresh. On --resume this stops an omitted flag
        (e.g. dropping --local-kind) from silently re-defaulting and flipping
        phase branches / invalidating hashes."""
        return replace(
            self,
            context=saved.get("context"),
            multi_cluster=saved.get("multi_cluster", self.multi_cluster),
            skip_recreate=saved.get("skip_recreate", self.skip_recreate),
            skip_evg=saved.get("skip_evg", self.skip_evg),
            skip_devcontainer=saved.get("skip_devcontainer", self.skip_devcontainer),
            skip_prepare_e2e=saved.get("skip_prepare_e2e", self.skip_prepare_e2e),
            force=saved.get("force", self.force),
            evg_host_name=saved.get("evg_host_name"),
            distro=saved.get("distro"),
            region=saved.get("region"),
            local_kind=saved.get("local_kind", self.local_kind),
            cluster_name=saved.get("cluster_name"),
            in_place=saved.get("in_place", self.in_place),
        )

    def worktree_path_or_main(self) -> Path:
        """Where the orchestrator state file lives — always the target worktree
        dir, so .generated/wt-ctl/ is removed with the worktree at teardown."""
        return self.worktree_path


# ---------------------------------------------------------------------------
# Phase
# ---------------------------------------------------------------------------


@dataclass
class Phase:
    name: str
    run: Callable[[], None]  # idempotent action
    input_hash: Callable[[], str]  # hash inputs
    skip: Callable[[], bool] = lambda: False  # static skip (e.g. --skip-evg)
    log_relpath: Optional[str] = None  # for state.log


# ---------------------------------------------------------------------------
# CreateOrchestrator
# ---------------------------------------------------------------------------


class CreateOrchestrator:
    """End-to-end ``wt-ctl create``."""

    def __init__(self, runner: Runner, inputs: CreateInputs) -> None:
        self.runner = runner
        self.inputs = inputs
        # Workhorse scripts live in main_repo_root (where the tool is invoked
        # from when the worktree doesn't exist yet) until the worktree is
        # created — at which point each phase runs scripts inside the new
        # worktree (cwd-anchored) so it has its own checked-out scripts.
        self.scripts = inputs.main_repo_root / "scripts" / "dev"
        # Same in-worktree scripts, used after worktree_init.
        self._wt_scripts = inputs.worktree_path / "scripts" / "dev"

    # ------------------------------------------------------------------
    # public entry point
    # ------------------------------------------------------------------
    def run(
        self,
        *,
        resume: bool = False,
        restart_from: Optional[str] = None,
        emit: Callable[[str], None] = lambda _msg: None,
    ) -> None:
        state = self._load_or_init_state(resume=resume, restart_from=restart_from)
        # An in_progress phase without --resume/--restart-from means a prior run
        # was killed uncleanly; picking up requires explicit acknowledgement.
        if not (resume or restart_from):
            stuck = state.has_in_progress() if state is not None else None
            if stuck:
                raise StateConflict(
                    f"phase '{stuck}' was left in_progress by a previous run; "
                    "re-run with --resume or --restart-from <phase>"
                )

        if state is None:
            # First invocation: build fresh state but DON'T save yet — writing
            # .generated/wt-ctl/state.json under the target path would make
            # `git worktree add` refuse to populate it. Persisted after worktree_init.
            state = ostate.OrchestratorState.initial(
                branch=self.inputs.branch,
                inputs=self.inputs.to_dict(),
            )
        else:
            # Resume / restart: keep existing state, refresh inputs so hash
            # comparisons run against the current command.
            state.inputs = self.inputs.to_dict()
            state.branch = self.inputs.branch

        phases = self._phases()
        plan = self._plan(state, phases, resume=resume, restart_from=restart_from)
        if not plan:
            emit("[wt-ctl] create: nothing to do (all phases ok).")
            return

        i = 0
        while i < len(plan):
            phase = plan[i]
            # Run the PARALLEL_GROUP pair concurrently when both are adjacent in the plan.
            if (
                phase.name == ostate.PARALLEL_GROUP[0]
                and i + 1 < len(plan)
                and plan[i + 1].name == ostate.PARALLEL_GROUP[1]
            ):
                self._run_parallel_pair(state, phase, plan[i + 1], emit=emit)
                i += 2
                continue
            self._run_one(state, phase, emit=emit)
            i += 1

        # Record intentionally-skipped phases as SKIPPED (not PENDING) so status
        # doesn't report "incomplete" and --resume won't re-run them.
        host = self.inputs.worktree_path_or_main()
        if host.is_dir():
            for phase in phases:
                if not phase.skip():
                    continue
                rec = state.phases.get(phase.name)
                if rec is None or rec.status not in (ostate.OK, ostate.SKIPPED):
                    state.set_status(phase.name, ostate.SKIPPED, input_hash=phase.input_hash())
            ostate.save(host, state)

        emit("[wt-ctl] create: done.")

    # ------------------------------------------------------------------
    # phase definitions
    # ------------------------------------------------------------------
    def _phases(self) -> list[Phase]:
        i = self.inputs
        wt = i.worktree_path
        log_dir = logs_dir(wt)

        def _h(payload: dict) -> str:
            return ostate.hash_inputs(payload)

        # ---- worktree_init ------------------------------------------------
        def _do_worktree_init() -> None:
            # Already inside the target worktree: skip create_worktree.sh but
            # still apply --context below.
            already_inside = i.in_place or (_is_same_path(self.runner_cwd_or_main(), wt) and wt.exists())
            if not already_inside:
                host = i.host_worktree_root
                argv = [
                    str(host / "scripts" / "dev" / "create_worktree.sh"),
                ]
                if i.force:
                    argv.append("-f")
                argv.append(i.branch)
                # create_worktree.sh reads PROJECT_DIR as the dir siblings live under.
                env = {"PROJECT_DIR": str(host)}
                # Log into the host worktree's logs dir (git worktree add refuses a
                # non-empty target), named per-target so parallel creates don't collide.
                host_log = logs_dir(host) / f"worktree_init-{i.branch_dir}.log"
                self.runner.run_streaming(
                    argv,
                    prefix="[worktree] ",
                    log_path=host_log,
                    env=env,
                    cwd=host,
                )
            # Switch context before initialize.sh renders compose.generated.yml.
            if i.context:
                self.runner.run_streaming(
                    ["make", "switch", f"context={i.context}"],
                    prefix="[ctx] ",
                    log_path=log_dir / "context_switch.log",
                    cwd=wt,
                )

        # ---- initialize_hook ---------------------------------------------
        def _do_initialize_hook() -> None:
            init_sh = wt / ".devcontainer" / "scripts" / "initialize.sh"
            if not init_sh.is_file():
                return
            self.runner.run_streaming(
                ["bash", str(init_sh)],
                prefix="[init] ",
                log_path=log_dir / "initialize_hook.log",
                cwd=wt,
            )

        # ---- net_allocate -------------------------------------------------
        def _do_net_allocate() -> None:
            env_file = wt / ".devcontainer" / ".env"
            existing_prefix = _read_existing_prefix(env_file)
            if existing_prefix is not None:
                # Prefix already pinned. If a prior run crashed before writing the
                # four derived vars (NET_X / NET_Y_BASE / NET_Y_VIP / PROXY_PORT),
                # compose.yml falls back to its ${VAR:-default} and clobbers other
                # worktrees' subnets — repair in place rather than re-allocating,
                # keeping the registry-owned index.
                missing_derived = _missing_derived_env_keys(env_file)
                if missing_derived:
                    params = stack_params(existing_prefix)
                    all_lines = env_lines_for(params)
                    key_to_line = {ln.split("=", 1)[0]: ln for ln in all_lines}
                    repair_lines = [
                        key_to_line[key] for key in DERIVED_ENV_KEYS if key in missing_derived and key in key_to_line
                    ]
                    if repair_lines:
                        with env_file.open("a") as fh:
                            cur = env_file.read_text()
                            if cur and not cur.endswith("\n"):
                                fh.write("\n")
                            fh.write("\n".join(repair_lines) + "\n")
                return
            block = NetworkDomain(self.runner, wt).allocate(i.branch_dir)
            if not block.startswith("MCK_DEVC_NET_PREFIX="):
                raise WtCtlError(f"NetworkDomain.allocate produced unexpected output: {block!r}")
            # Append the 5-line block, preserving any other keys.
            env_file.parent.mkdir(parents=True, exist_ok=True)
            with env_file.open("a") as fh:
                # Guard against a hand-edited .env lacking a trailing newline.
                if env_file.exists():
                    cur = env_file.read_text()
                    if cur and not cur.endswith("\n"):
                        fh.write("\n")
                fh.write(block)

        # ---- evg_prepare --------------------------------------------------
        def _do_evg_prepare() -> None:
            if i.local_kind:
                # Local-kind: kind cluster on this laptop, no EVG host.
                #   1. drop any stale EVG-host pin so root-context's EVG_HOST_NAME
                #      resolution doesn't inherit a defunct host's identity.
                #   2. recreate_kind_cluster.sh writes .generated/current.kubeconfig
                #      and keeps DELETE_KIND_NETWORK=false to spare sibling worktrees.
                #   3. make switch regenerates context env so CLUSTER_NAME (derived
                #      from the kubeconfig's current-context) lands in context.env.
                generated = wt / ".generated"
                generated.mkdir(parents=True, exist_ok=True)
                for f in (".current-evg-host", ".current-evg-host-address"):
                    p = generated / f
                    if p.is_file():
                        p.unlink()
                if i.multi_cluster:
                    # Switch to the context FIRST: it defines the member/central
                    # cluster names and MetalLB ranges recreate_kind_clusters.sh
                    # reads via set_env_context. All 5 kind clusters share the
                    # `kind` docker network; the devc reaches each apiserver at
                    # its node-container IP (kubeconfig rewrite + dc_up net join).
                    mc_ctx = i.context or "e2e_multi_cluster_kind"
                    self.runner.run_streaming(
                        ["make", "switch", f"context={mc_ctx}"],
                        prefix="[local-kind] ",
                        log_path=log_dir / "evg_prepare.log",
                        cwd=wt,
                    )
                    if not i.skip_recreate:
                        self.runner.run_streaming(
                            [str(wt / "scripts" / "dev" / "recreate_kind_clusters.sh")],
                            prefix="[local-kind] ",
                            log_path=log_dir / "evg_prepare.log",
                            env={"DELETE_KIND_NETWORK": "false"},
                            cwd=wt,
                        )
                    else:
                        sys.stderr.write("[local-kind] --skip-recreate set; skipping recreate_kind_clusters.sh\n")
                else:
                    cluster = i.resolved_cluster_name()
                    if not i.skip_recreate:
                        self.runner.run_streaming(
                            [str(wt / "scripts" / "dev" / "recreate_kind_cluster.sh"), cluster],
                            prefix="[local-kind] ",
                            log_path=log_dir / "evg_prepare.log",
                            env={"DELETE_KIND_NETWORK": "false"},
                            cwd=wt,
                        )
                    else:
                        sys.stderr.write("[local-kind] --skip-recreate set; skipping recreate_kind_cluster.sh\n")
                    # Re-derive context env against the now-current kubeconfig.
                    current_ctx_file = generated / ".current_context"
                    if current_ctx_file.is_file():
                        ctx = current_ctx_file.read_text().strip()
                        self.runner.run_streaming(
                            ["make", "switch", f"context={ctx}"],
                            prefix="[local-kind] ",
                            log_path=log_dir / "evg_prepare.log",
                            cwd=wt,
                        )
                # On-host kfp registration (the devc-side registration runs later
                # in the `kubeconfig` phase). refresh needs CLUSTER_NAME /
                # K8S_FWD_PROXY / MCK_DEVC_* from the just-regenerated context env.
                env_for_refresh = _load_context_env(wt)
                from .domains.kubeconfig import KubeconfigDomain  # local import avoids a module cycle

                KubeconfigDomain(self.runner).refresh(
                    wt,
                    env=env_for_refresh,
                    in_devc=False,
                    emit=lambda m: sys.stderr.write(f"[local-kind] {m}\n"),
                )
                return
            argv = [
                str(wt / "scripts" / "dev" / "evg_prepare.sh"),
                "--name",
                i.resolved_evg_host_name(),
            ]
            if i.multi_cluster:
                argv.append("--multi")
            if i.skip_recreate:
                argv.append("--skip-recreate")
            if i.distro:
                argv += ["--distro", i.distro]
            if i.region:
                argv += ["--region", i.region]
            self.runner.run_streaming(
                argv,
                prefix="[evg-host] ",
                log_path=log_dir / "evg_prepare.log",
                cwd=wt,
            )

        # ---- dc_build -----------------------------------------------------
        def _do_dc_build() -> None:
            argv = ["devcontainer", "build", "--workspace-folder", str(wt)]
            self.runner.run_streaming(
                argv,
                prefix="[build] ",
                log_path=log_dir / "dc_build.log",
                cwd=wt,
            )

        # ---- dc_up --------------------------------------------------------
        def _do_dc_up() -> None:
            # `compose down` the whole project first so `devcontainer up` rebuilds
            # a fresh stack against current state. `devcontainer up` alone no-ops
            # when a container exists, and --remove-existing-container only tears
            # down the devcontainer service + its depends_on chain (k8s-proxy),
            # leaving siblings like gost-proxy on stale config.
            # project name = worktree basename (what the CLI uses), not branch_dir
            # — they diverge under --in-place.
            project = project_name_for(wt)
            ps = self.runner.run(
                ["docker", "compose", "-p", project, "ps", "--format", "{{.Name}}"],
                check=False,
                cwd=wt,
            )
            if ps.rc == 0 and ps.stdout.strip():
                self.runner.run_streaming(
                    ["docker", "compose", "-p", project, "down", "--remove-orphans"],
                    prefix="[up:down] ",
                    log_path=log_dir / "dc_up.log",
                    cwd=wt,
                )

            argv = ["devcontainer", "up", "--workspace-folder", str(wt)]
            self.runner.run_streaming(
                argv,
                prefix="[up] ",
                log_path=log_dir / "dc_up.log",
                cwd=wt,
            )
            # Linux local-kind: kind binds the apiserver to 127.0.0.1:<hostport>,
            # unreachable from the devc. Join devc + k8s-proxy to the `kind`
            # network so they reach each node container at <node-ip>:6443 (what
            # the kubeconfig phase rewrites to). No-op on macOS / EVG-host paths.
            if i.local_kind and platform.system() == "Linux":
                self._join_kind_network(wt, project, log_dir / "dc_up.log")

        # ---- reconcile ----------------------------------------------------
        def _do_reconcile() -> None:
            user_yml = wt / ".devcontainer" / "compose.user.yml"
            if not user_yml.is_file() or user_yml.stat().st_size == 0:
                return  # no overrides → nothing to reconcile
            # Reconcile the auxiliary sidecars only. Force-recreating the main
            # `devcontainer` service via raw compose desyncs the CLI (later
            # `devcontainer exec` fails with "Dev container not found").
            services = [s for s in _services_in(user_yml) if s != "devcontainer"]
            if not services:
                return
            compose = wt / ".devcontainer" / "compose.yml"
            generated = wt / ".devcontainer" / "compose.generated.yml"
            project = project_name_for(wt)
            base = ["docker", "compose", "-p", project, "-f", str(compose)]
            if generated.is_file():
                base += ["-f", str(generated)]
            base += ["-f", str(user_yml)]
            for svc in services:
                argv = base + ["up", "-d", "--force-recreate", "--no-deps", svc]
                self.runner.run_streaming(
                    argv,
                    prefix=f"[reconcile {svc}] ",
                    log_path=log_dir / "reconcile.log",
                    cwd=wt,
                )

        # ---- post_start ---------------------------------------------------
        def _do_post_start() -> None:
            cmd = (
                "if [[ ! -S /ssh-agent/socket ]]; then "
                "echo 'ssh-agent socket missing; re-running post-start.sh'; "
                "bash /workspace/.devcontainer/scripts/post-start.sh; "
                "else echo 'ssh-agent socket already present'; fi"
            )
            self.runner.run_streaming(
                _devc_bash(wt, cmd),
                prefix="[post-start] ",
                log_path=log_dir / "post_start.log",
                cwd=wt,
            )

        # ---- kubeconfig ---------------------------------------------------
        def _do_kubeconfig() -> None:
            # `bash -lc` is a login shell but shell-init.sh only auto-sources
            # devenv for interactive shells, so source it explicitly to export
            # PROJECT_DIR / KUBECONFIG before `make`. site-context picks
            # KUBECONFIG by side alone, so `make switch` yields the right
            # context.devc.env. The refresh works for both EVG-host and
            # local-kind, whichever populated .generated/current.kubeconfig.
            cmd = (
                "set -Eeou pipefail; "
                "cd /workspace; "
                "if [[ -f scripts/dev/devenv ]]; then . scripts/dev/devenv 2>/dev/null || true; fi; "
                'make switch context="$(cat .generated/.current_context)"; '
                "scripts/dev/wt-ctl --quiet kubeconfig refresh"
            )
            self.runner.run_streaming(
                _devc_bash(wt, cmd),
                prefix="[refresh] ",
                log_path=log_dir / "refresh_kubeconfig.log",
                cwd=wt,
            )

        # ---- prepare_e2e --------------------------------------------------
        def _do_prepare_e2e() -> None:
            # dc_up is hash-cached, so on --resume its one-shot kind-network join
            # never re-runs — but a compose recreate drops the attachment. Re-apply.
            if i.local_kind and platform.system() == "Linux":
                self._join_kind_network(wt, project_name_for(wt), log_dir / "dc_up.log")
            # prepare-local-e2e's first kubectl request is unbuffered and can race
            # k8s-proxy / autossh stabilisation right after up, 503-ing the whole
            # phase. Poll readyz until 200 first.
            self._wait_apiserver_ready(
                wt,
                deadline_s=60.0,
                interval_s=2.0,
            )
            # Source devenv (as in the kubeconfig phase): prepare-local-e2e's
            # scripts expect KUBECONFIG / PROJECT_DIR / context.env exported.
            self.runner.run_streaming(
                _devc_bash(
                    wt,
                    "set -Eeou pipefail; " "cd /workspace; " ". scripts/dev/devenv; " "make prepare-local-e2e",
                ),
                prefix="[prepare-e2e] ",
                log_path=log_dir / "prepare_local_e2e.log",
                cwd=wt,
            )
            # local-kind: prepare-local-e2e's in-devc refresh can't resolve kind
            # node IPs (no docker in the container). Re-run refresh host-side so
            # the member servers get rewritten to node IPs.
            if i.local_kind:
                from .domains.kubeconfig import KubeconfigDomain  # local import avoids a module cycle

                KubeconfigDomain(self.runner).refresh(
                    wt,
                    env=_load_context_env(wt),
                    in_devc=False,
                    emit=lambda m: sys.stderr.write(f"[local-kind] {m}\n"),
                )

        # ---- op_run ------------------------------------------------------
        def _do_op_run() -> None:
            # dc_up's compose-down killed any prior operator, so launch op_run.sh
            # detached to leave the operator up against the current kubeconfig.
            # Then kill the `mck` tmux session so the next attach reloads the
            # layout fresh (a cached one pins stale kubeconfig handles / zsh
            # command-hash). Both calls soft-fail so create still finishes.
            self.runner.run_streaming(
                _devc_bash(
                    wt,
                    "set -Eeou pipefail; "
                    "cd /workspace; "
                    ". scripts/dev/devenv; "
                    "scripts/dev/op_run.sh --detach || "
                    "echo '[op_run] start failed (continuing; run op_run.sh manually)'; "
                    "tmux kill-session -t mck 2>/dev/null && "
                    "echo '[op_run] killed stale mck tmux session; next wt-ctl attach will re-load tmuxp' || "
                    "echo '[op_run] no mck tmux session to refresh'",
                ),
                prefix="[op_run] ",
                log_path=log_dir / "op_run.log",
                cwd=wt,
            )

        # The hash payloads are intentionally narrow: only fields that
        # *actually* affect the phase get hashed, so flipping unrelated flags
        # (e.g. --multi-cluster) doesn't invalidate net_allocate.
        return [
            Phase(
                name="worktree_init",
                run=_do_worktree_init,
                input_hash=lambda: _h({"branch": i.branch, "force": i.force}),
                log_relpath="logs/setup_worktree/worktree_init.log",
            ),
            Phase(
                name="initialize_hook",
                run=_do_initialize_hook,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/initialize_hook.log",
            ),
            Phase(
                # Hash includes the env-file's completeness: if a prior run wrote
                # only MCK_DEVC_NET_PREFIX and crashed before the derived lines,
                # resume must re-enter to append them. Hashing by branch_dir alone
                # would keep the phase ok and let compose fall back to its default
                # /23, colliding with sibling worktrees.
                name="net_allocate",
                run=_do_net_allocate,
                input_hash=lambda: _h(
                    {
                        "branch_dir": i.branch_dir,
                        "missing_derived": _missing_derived_env_keys(
                            i.worktree_path_or_main() / ".devcontainer" / ".env",
                        ),
                    }
                ),
                skip=lambda: i.skip_devcontainer,
            ),
            Phase(
                name="evg_prepare",
                run=_do_evg_prepare,
                input_hash=lambda: _h(
                    {
                        "evg_host_name": i.resolved_evg_host_name(),
                        "multi_cluster": i.multi_cluster,
                        "skip_recreate": i.skip_recreate,
                        "local_kind": i.local_kind,
                        "cluster_name": i.resolved_cluster_name() if i.local_kind else None,
                    }
                ),
                skip=lambda: i.skip_evg,
                log_relpath="logs/setup_worktree/evg_prepare.log",
            ),
            Phase(
                name="dc_build",
                run=_do_dc_build,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/dc_build.log",
            ),
            Phase(
                name="dc_up",
                run=_do_dc_up,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/dc_up.log",
            ),
            Phase(
                name="reconcile",
                run=_do_reconcile,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/reconcile.log",
            ),
            Phase(
                name="post_start",
                run=_do_post_start,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/post_start.log",
            ),
            Phase(
                name="kubeconfig",
                run=_do_kubeconfig,
                input_hash=lambda: _h(
                    {
                        "branch_dir": i.branch_dir,
                        "context": i.context or "",
                    }
                ),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/refresh_kubeconfig.log",
            ),
            Phase(
                name="prepare_e2e",
                run=_do_prepare_e2e,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
                skip=lambda: i.skip_devcontainer or i.skip_prepare_e2e,
                log_relpath="logs/setup_worktree/prepare_local_e2e.log",
            ),
            Phase(
                name="op_run",
                run=_do_op_run,
                # Time-based hash so the state.json skip never short-circuits this
                # phase: reaching the tail of the plan always restarts the operator
                # against the current kubeconfig and re-wires tmux, even on a no-op
                # resume (dc_up's compose-down nuked both). op_run.sh is idempotent.
                input_hash=lambda: _h({"ts": time.time_ns()}),
                skip=lambda: i.skip_devcontainer,
                log_relpath="logs/setup_worktree/op_run.log",
            ),
        ]

    # ------------------------------------------------------------------
    # plan
    # ------------------------------------------------------------------
    def _plan(
        self,
        state: ostate.OrchestratorState,
        phases: list[Phase],
        *,
        resume: bool,
        restart_from: Optional[str],
    ) -> list[Phase]:
        """Decide which phases to run this invocation.

        * ``--restart-from <phase>`` clears that phase and later, then runs every
          non-skipped phase from there.
        * ``--resume`` runs from the first phase that isn't OK or whose input_hash
          no longer matches; skipped phases are passed over silently.
        * Default: run every non-skipped phase (all are idempotent).
        """
        if restart_from:
            state.clear_from(restart_from)
            host = self.inputs.worktree_path_or_main()
            if host.is_dir():
                ostate.save(host, state)

        if resume:
            # Find the earliest non-OK or hash-mismatched phase among
            # non-skipped phases.
            first_idx: Optional[int] = None
            for idx, phase in enumerate(phases):
                if phase.skip():
                    continue
                rec = state.phases.get(phase.name)
                # A phase recorded SKIPPED by an earlier run stays skipped on
                # resume even if the opt-out flag was dropped — otherwise
                # resume would spawn the EVG host the user opted out of. Force
                # it explicitly with --restart-from <phase>.
                if rec is not None and rec.status == ostate.SKIPPED:
                    continue
                want_hash = phase.input_hash()
                if rec is None or rec.status != ostate.OK or rec.input_hash != want_hash:
                    first_idx = idx
                    break
            if first_idx is None:
                return []
            return [p for p in phases[first_idx:] if not p.skip()]

        # Default / restart-from: run every non-skipped phase from the
        # current cursor (post-restart-from) onward. For default (no flags),
        # the cursor is index 0 — every phase runs (idempotent).
        if restart_from:
            try:
                first_idx = ostate.PHASE_ORDER.index(restart_from)
            except ValueError as exc:
                raise StateConflict(f"unknown phase: {restart_from}") from exc
            relevant = phases[first_idx:]
        else:
            relevant = list(phases)
        return [p for p in relevant if not p.skip()]

    # ------------------------------------------------------------------
    # execution helpers
    # ------------------------------------------------------------------
    def _run_one(
        self,
        state: ostate.OrchestratorState,
        phase: Phase,
        *,
        emit: Callable[[str], None],
    ) -> None:
        emit(f"[wt-ctl] phase: {phase.name}")
        # Save lazily: pre-worktree_init the target dir doesn't exist, and writing
        # .generated/wt-ctl/ there would stop git worktree add from populating it.
        host = self.inputs.worktree_path_or_main()
        if host.is_dir():
            state.set_status(phase.name, ostate.IN_PROGRESS, log=phase.log_relpath)
            ostate.save(host, state)
        try:
            phase.run()
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            state.set_status(
                phase.name,
                ostate.FAILED,
                input_hash=phase.input_hash(),
                log=phase.log_relpath,
            )
            # The worktree may now exist (worktree_init failed mid-way) — try
            # to save; tolerate a missing host dir silently.
            if host.is_dir():
                ostate.save(host, state)
            raise
        state.set_status(
            phase.name,
            ostate.OK,
            input_hash=phase.input_hash(),
            log=phase.log_relpath,
        )
        # After worktree_init succeeds the dir exists — save now persists
        # the just-completed phase's record.
        if host.is_dir():
            ostate.save(host, state)

    def _run_parallel_pair(
        self,
        state: ostate.OrchestratorState,
        a: Phase,
        b: Phase,
        *,
        emit: Callable[[str], None],
    ) -> None:
        """Run two phases in worker threads, recording each individually."""
        emit(f"[wt-ctl] parallel phases: {a.name} + {b.name}")
        host = self.inputs.worktree_path_or_main()
        for ph in (a, b):
            state.set_status(ph.name, ostate.IN_PROGRESS, log=ph.log_relpath)
        if host.is_dir():
            ostate.save(host, state)

        # Thread directly (not Runner.run_parallel) to capture each phase's failure
        # and let both finish before surfacing them via ParallelPhaseFailures.
        results: dict[str, Optional[Exception]] = {a.name: None, b.name: None}

        import threading

        def _worker(phase: Phase) -> None:
            try:
                phase.run()
            except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
                results[phase.name] = exc
            except Exception as exc:  # noqa: BLE001
                # Record unexpected exceptions too; otherwise the phase would be
                # marked OK and --resume would silently skip the crash.
                results[phase.name] = WtCtlError(f"unexpected error in phase {phase.name}: {exc!r}")

        ta = threading.Thread(target=_worker, args=(a,))
        tb = threading.Thread(target=_worker, args=(b,))
        ta.start()
        tb.start()
        ta.join()
        tb.join()

        for ph in (a, b):
            status = ostate.OK if results[ph.name] is None else ostate.FAILED
            state.set_status(ph.name, status, input_hash=ph.input_hash(), log=ph.log_relpath)
        if host.is_dir():
            ostate.save(host, state)

        failures = {name: err for name, err in results.items() if err is not None}
        if failures:
            ecfs = {name: err for name, err in failures.items() if isinstance(err, ExternalCommandFailed)}
            if ecfs:
                raise ParallelPhaseFailures(failures=ecfs)
            for err in failures.values():
                raise err

    # ------------------------------------------------------------------
    # state file location helpers
    # ------------------------------------------------------------------
    def _load_or_init_state(
        self,
        *,
        resume: bool,
        restart_from: Optional[str],
    ) -> Optional[ostate.OrchestratorState]:
        host = self.inputs.worktree_path_or_main()
        # Don't mkdir here — git worktree add refuses a non-empty target; save()
        # creates .generated/wt-ctl/ once worktree_init has populated the dir.
        if not host.is_dir():
            return None
        return ostate.load(host)

    def runner_cwd_or_main(self) -> Path:
        """Where the user invoked wt-ctl from. Used to detect "already inside
        the target worktree" so worktree_init can short-circuit.
        """
        try:
            return Path(os.getcwd()).resolve()
        except OSError:
            return self.inputs.main_repo_root

    def _wait_apiserver_ready(
        self,
        wt: Path,
        *,
        deadline_s: float,
        interval_s: float,
    ) -> None:
        """Poll ``kubectl get --raw=/readyz`` in the devcontainer until it
        returns 200, raising ``WtCtlError`` on timeout. Probe output goes to
        stderr only (the phase's run_streaming owns the phase log)."""
        argv = _devc_bash(
            wt,
            "set -Eeou pipefail; "
            "cd /workspace; "
            ". scripts/dev/devenv; "
            "kubectl --request-timeout=3s get --raw=/readyz",
        )
        deadline = time.monotonic() + deadline_s
        attempts = 0
        last_stderr = ""
        while True:
            attempts += 1
            result = self.runner.run(argv, check=False, cwd=wt)
            if result.rc == 0:
                sys.stderr.write(
                    f"[prepare-e2e] apiserver ready after " f"{attempts} probe{'' if attempts == 1 else 's'}\n"
                )
                return
            last_stderr = (result.stderr or result.stdout or "").strip()
            # Liveness tick every 5 probes so a slow handshake isn't silent.
            if attempts % 5 == 0:
                sys.stderr.write(
                    f"[prepare-e2e] still waiting for apiserver "
                    f"(probe {attempts}, last: {last_stderr or '(empty)'})\n"
                )
            if time.monotonic() >= deadline:
                raise WtCtlError(
                    f"apiserver readyz never returned 200 after "
                    f"{int(deadline_s)}s ({attempts} probes); last stderr: "
                    f"{last_stderr or '(empty)'}"
                )
            time.sleep(interval_s)

    def _join_kind_network(self, wt: Path, project: str, log_path: Path) -> None:
        """Join the devc + k8s-proxy containers to the shared ``kind`` docker
        network (Linux local-kind only). Idempotent — skips a container already
        attached. Best-effort: a failure is logged, not fatal, since the
        kubeconfig phase's readyz probe will surface an unreachable apiserver."""
        for svc in ("devcontainer", "k8s-proxy"):
            ps = self.runner.run(["docker", "compose", "-p", project, "ps", "-q", svc], check=False, cwd=wt)
            cid = (ps.stdout.strip().splitlines() or [""])[0]
            if not cid:
                sys.stderr.write(f"[kind-net] no running container for service {svc!r}; skipping\n")
                continue
            # `{{if}}` yields "yes" only when the endpoint pointer is non-nil.
            # `{{index ...}}` alone prints "<nil>" for an absent key, which is
            # truthy as a string — so gate the check with `{{if}}`.
            attached = self.runner.run(
                ["docker", "inspect", "-f", '{{if index .NetworkSettings.Networks "kind"}}yes{{end}}', cid],
                check=False,
            ).stdout.strip()
            if attached == "yes":
                sys.stderr.write(f"[kind-net] {svc} already on kind network\n")
                continue
            res = self.runner.run(["docker", "network", "connect", "kind", cid], check=False)
            if res.rc == 0:
                sys.stderr.write(f"[kind-net] joined {svc} to kind network\n")
            else:
                sys.stderr.write(f"[kind-net] WARN: failed to join {svc} to kind network: {res.stderr.strip()}\n")


# ---------------------------------------------------------------------------
# DeleteOrchestrator
# ---------------------------------------------------------------------------


@dataclass
class DeleteInputs:
    """Opt-in delete targets; every flag defaults False, so ``wt-ctl delete``
    with no flags is a no-op. Git branches are never deleted (branch is
    metadata only)."""

    branch: str
    branch_dir: str
    worktree_path: Path
    main_repo_root: Path
    delete_om: bool = False
    delete_devc: bool = False
    delete_evg: bool = False
    delete_worktree: bool = False
    evg_host_name: Optional[str] = None

    def resolved_evg_host_name(self) -> str:
        return self.evg_host_name or self.branch_dir

    def any_target(self) -> bool:
        return any(
            [
                self.delete_om,
                self.delete_devc,
                self.delete_evg,
                self.delete_worktree,
            ]
        )


class DeleteOrchestrator:
    """Opt-in teardown. Each step runs only when its target flag is set;
    failures log and continue so other targets still run. Git branches are
    never deleted — worktrees are cheap to recreate, lost work isn't."""

    def __init__(self, runner: Runner, inputs: DeleteInputs) -> None:
        self.runner = runner
        self.inputs = inputs
        self.scripts = inputs.main_repo_root / "scripts" / "dev"

    def run(self, *, emit: Callable[[str], None] = lambda _msg: None) -> None:
        i = self.inputs
        targets = (
            ", ".join(
                t
                for t, on in [
                    ("om", i.delete_om),
                    ("devc", i.delete_devc),
                    ("evg", i.delete_evg),
                    ("worktree", i.delete_worktree),
                ]
                if on
            )
            or "(none)"
        )
        evg_pin = i.worktree_path / ".generated" / ".current-evg-host"
        evg_tag = f"evg_host={i.resolved_evg_host_name()}" if evg_pin.is_file() else "local"
        emit(f"[wt-ctl] delete: branch={i.branch} targets=[{targets}] " f"worktree={i.worktree_path} {evg_tag}")
        if i.delete_om:
            self._step_om_clean(emit)
        if i.delete_devc:
            self._step_compose_down(emit)
        if i.delete_evg:
            self._step_evg_terminate(emit)
        if i.delete_worktree:
            self._step_worktree_remove(emit)
            # Network registry entry is tied to the worktree's net prefix;
            # release it only when the worktree itself goes away.
            self._step_prefix_release(emit)
        emit("[wt-ctl] delete: done")

    # ------------------------------------------------------------------
    def _step_om_clean(self, emit: Callable[[str], None]) -> None:
        wt = self.inputs.worktree_path
        if not wt.is_dir():
            emit("[wt-ctl] om: skipped (worktree dir gone)")
            return
        env_file = wt / ".devcontainer" / ".env"
        prefix = _read_existing_prefix(env_file)
        if prefix is None:
            emit("[wt-ctl] om: skipped (no MCK_DEVC_NET_PREFIX in .devcontainer/.env)")
            return
        emit("[wt-ctl] om: deleting cloud-qa OM projects scoped to this worktree")
        try:
            self.runner.run_streaming(
                [str(wt / "scripts" / "dev" / "delete_om_projects.sh")],
                prefix="[om-clean] ",
                cwd=wt,
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] om: cleanup hit an error (continuing): {exc.render()}")

    def _step_compose_down(self, emit: Callable[[str], None]) -> None:
        project = project_name_for(self.inputs.worktree_path)
        try:
            ps = self.runner.run(
                ["docker", "compose", "-p", project, "ps", "--format", "{{.Name}}"],
                check=False,
            )
        except ToolMissing:
            emit("[wt-ctl] compose: docker unavailable — skipping stack teardown")
            return
        if ps.rc != 0 or not ps.stdout.strip():
            emit(f"[wt-ctl] compose: no running stack for project '{project}'")
            return
        emit(f"[wt-ctl] compose: stopping stack '{project}'")
        try:
            self.runner.run_streaming(
                ["docker", "compose", "-p", project, "down", "--remove-orphans"],
                prefix="[compose-down] ",
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] compose: down failed (continuing): {exc.render()}")

    def _read_current_context(self, kubeconfig: Path) -> Optional[str]:
        """Return the kubeconfig's ``current-context``, or None if unreadable /
        malformed / unset. yaml is imported lazily so ``wt-ctl delete`` still
        runs (minus kind teardown) when pyyaml isn't installed yet."""
        try:
            import yaml  # type: ignore  # pyyaml — venv dep, may be missing on fresh runners
        except ImportError:
            return None
        try:
            data = yaml.safe_load(kubeconfig.read_text())
        except (OSError, yaml.YAMLError):
            return None
        if not isinstance(data, dict):
            return None
        value = data.get("current-context")
        if isinstance(value, str) and value:
            return value
        return None

    def _step_evg_terminate(self, emit: Callable[[str], None]) -> None:
        # No `.current-evg-host` pinned → local-kind or BYOC. Delete the kind
        # cluster named by the kubeconfig's current-context when it exists;
        # otherwise no-op (BYOC tears itself down).
        wt = self.inputs.worktree_path
        evg_pin = wt / ".generated" / ".current-evg-host"
        if not evg_pin.is_file():
            kc_path = wt / ".generated" / "current.kubeconfig"
            if not kc_path.is_file():
                emit("[wt-ctl] evg: no .current-evg-host and no current.kubeconfig; nothing to tear down")
                return
            ctx = self._read_current_context(kc_path)
            if not ctx or not ctx.startswith("kind-"):
                emit(
                    f"[wt-ctl] evg: kubeconfig current-context={ctx!r} is not a kind cluster (BYOC?); skipping teardown"
                )
                return
            cluster = ctx[len("kind-") :]
            if not self.runner.have("kind"):
                emit("[wt-ctl] evg: kind CLI not on PATH — skipping local-kind teardown")
                return
            listing = self.runner.run(["kind", "get", "clusters"], check=False)
            if listing.rc != 0 or cluster not in (listing.stdout or "").splitlines():
                emit(f"[wt-ctl] evg: kind cluster '{cluster}' not in `kind get clusters`; skipping (already gone?)")
                return
            emit(f"[wt-ctl] evg: deleting local kind cluster '{cluster}'")
            try:
                self.runner.run_streaming(
                    ["kind", "delete", "cluster", "--name", cluster],
                    prefix="[kind-delete] ",
                )
            except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
                emit(f"[wt-ctl] evg: kind delete failed (continuing): {exc.render()}")
            return

        if not self.runner.have("evergreen"):
            emit("[wt-ctl] evg: evergreen CLI not on PATH — skipping termination")
            return
        res = self.runner.run(
            ["evergreen", "host", "list", "--mine", "--json"],
            check=False,
        )
        if res.rc != 0:
            emit(f"[wt-ctl] evg: host list failed rc={res.rc} — skipping termination")
            return
        host_id = _find_host_id_by_name(
            res.stdout or "[]",
            self.inputs.resolved_evg_host_name(),
        )
        if host_id is None:
            emit(f"[wt-ctl] evg: no running host with displayName " f"'{self.inputs.resolved_evg_host_name()}'")
            return
        emit(f"[wt-ctl] evg: terminating " f"'{self.inputs.resolved_evg_host_name()}' (host_id={host_id})")
        try:
            self.runner.run_streaming(
                ["evergreen", "host", "terminate", "--host", host_id],
                prefix="[evg-term] ",
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] evg: terminate failed (continuing): {exc.render()}")

    def _step_worktree_remove(self, emit: Callable[[str], None]) -> None:
        wt = self.inputs.worktree_path
        # Move cwd off the worktree we're about to delete — otherwise a later
        # os.getcwd() / subprocess raises FileNotFoundError once the dir vanishes.
        try:
            cwd = os.getcwd()
            if Path(cwd).resolve() == wt.resolve() or wt in Path(cwd).resolve().parents:
                os.chdir(wt.parent)
        except (FileNotFoundError, OSError):
            os.chdir(Path.home())
        if wt.is_dir():
            emit(f"[wt-ctl] worktree: removing {wt}")
            try:
                self.runner.run(
                    [
                        "git",
                        "-C",
                        str(self.inputs.main_repo_root),
                        "worktree",
                        "remove",
                        "--force",
                        str(wt),
                    ],
                )
            except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
                emit(f"[wt-ctl] worktree: remove failed (continuing): {exc.render()}")
        else:
            emit(f"[wt-ctl] worktree: dir {wt} doesn't exist; skipping")
        try:
            self.runner.run(
                ["git", "-C", str(self.inputs.main_repo_root), "worktree", "prune"],
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] worktree: prune failed (continuing): {exc.render()}")

    def _step_prefix_release(self, emit: Callable[[str], None]) -> None:
        emit(f"[wt-ctl] network: releasing registry entry for '{self.inputs.branch_dir}'")
        try:
            # release() ignores worktree_parent, so main_repo_root (always set) is fine.
            domain = NetworkDomain(self.runner, self.inputs.main_repo_root)
            output = domain.release(self.inputs.branch_dir).rstrip("\n")
            if output:
                emit(f"[net-release] {output}")
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] network: release failed (continuing): {exc.render()}")


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------


def _is_same_path(a: Path, b: Path) -> bool:
    try:
        return a.resolve() == b.resolve()
    except OSError:
        return False


def _read_existing_prefix(env_file: Path) -> Optional[int]:
    if not env_file.is_file():
        return None
    try:
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if line.startswith("MCK_DEVC_NET_PREFIX="):
                value = line.split("=", 1)[1]
                if value.isdigit():
                    return int(value)
    except OSError:
        return None
    return None


def _missing_derived_env_keys(env_file: Path) -> list[str]:
    """``DERIVED_ENV_KEYS`` entries absent from ``env_file`` — lets net_allocate
    resume recover from a partial first-write of only ``MCK_DEVC_NET_PREFIX``."""
    if not env_file.is_file():
        return list(DERIVED_ENV_KEYS)
    try:
        text = env_file.read_text()
    except OSError:
        return list(DERIVED_ENV_KEYS)
    present: set[str] = set()
    for line in text.splitlines():
        line = line.strip()
        for key in DERIVED_ENV_KEYS:
            if line.startswith(f"{key}="):
                present.add(key)
    return [k for k in DERIVED_ENV_KEYS if k not in present]


def _services_in(compose_yml: Path) -> list[str]:
    """Top-level service keys under ``services:``. Stdlib-only (no PyYAML);
    relies on compose.user.yml's consistent 2-space indentation."""
    if not compose_yml.is_file():
        return []
    try:
        text = compose_yml.read_text()
    except OSError:
        return []
    services: list[str] = []
    in_services = False
    for raw in text.splitlines():
        if raw.strip().startswith("#"):
            continue
        if not raw.strip():
            continue
        if raw.startswith("services:"):
            in_services = True
            continue
        if in_services:
            # New top-level key ends the services block.
            if raw[:1] not in (" ", "\t"):
                in_services = False
                continue
            stripped = raw.lstrip()
            indent = len(raw) - len(stripped)
            # services children are indented at 2 spaces; nested keys are 4+.
            if indent == 2 and stripped.endswith(":"):
                services.append(stripped[:-1])
    return services


def _find_host_id_by_name(json_text: str, name: str) -> Optional[str]:
    """Parse `evergreen host list --json` and return the id of the running
    host whose name matches. Skips terminated/decommissioned hosts.
    """
    import json

    try:
        data = json.loads(json_text or "[]")
    except json.JSONDecodeError:
        return None
    if not isinstance(data, list):
        return None
    dead = {"terminated", "decommissioned"}
    for h in data:
        if not isinstance(h, dict):
            continue
        if h.get("name") != name:
            continue
        status = (h.get("status") or "").lower()
        if status in dead:
            continue
        host_id = h.get("id")
        if host_id:
            return host_id
    return None
