"""``wt-ctl create`` and ``wt-ctl delete`` orchestrators.

Implements Phase 2: a data-driven phase pipeline with state-tracking + resume
for ``create``, and a forward-only best-effort teardown for ``delete``. Both
take a Runner and a worktree path; neither imports subprocess directly.

Design:

* ``Phase`` is a dataclass: name, callable, input-hash function. The runner
  iterates over a tuple of Phases, deciding skip / restart / fail / record
  in one place — no per-phase branching outside the Phase definitions.
* The parallel pair (``evg_prepare`` + ``dc_build``) is detected by name
  before the loop ticks; if both are non-OK at resume time they're launched
  through ``Runner.run_parallel`` and recorded individually. If only one
  needs re-running (e.g. on resume after dc_build failed) it runs alone.
* The ``DeleteOrchestrator`` mirrors ``wt_teardown.sh`` step-for-step. Each
  step is best-effort (logged + continued on failure). No state file.
"""

from __future__ import annotations

import os
import sys
import time
from dataclasses import dataclass, field
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
    """Parse the just-generated .generated/context*.env files into a dict
    so we can pass it to in-process KubeconfigDomain.refresh without
    requiring the orchestrator's own shell env to be pre-loaded. Files are
    plain KEY=value (potentially quoted). Lines starting with `#` and
    blanks are skipped.
    """
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
    """Build the canonical ``devcontainer exec ... bash -lc <script>`` argv.

    Every devcontainer-exec call site in this module uses the same wrapper:
    a non-interactive login shell (``bash -lc``) targeting the worktree's
    devcontainer workspace. The script string is the inner shell command —
    typically a ``set -Eeou pipefail; cd /workspace; . scripts/dev/devenv;
    <action>`` chain.
    """
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
    """All flags passed to ``wt-ctl create``. Stored in state.json verbatim
    so resume can detect changed inputs and invalidate hashes.
    """

    branch: str
    branch_dir: str
    worktree_path: Path  # target worktree root (may not yet exist)
    main_repo_root: Path  # the main repo (parent of .git/common dir)
    host_worktree_root: Path  # the worktree the user invoked from; used
    # as PROJECT_DIR for create_worktree.sh so
    # the new worktree is its sibling. Falls
    # back to main_repo_root when outside any
    # worktree.
    context: Optional[str] = None
    multi_cluster: bool = False
    skip_recreate: bool = False
    skip_evg: bool = False
    skip_devcontainer: bool = False
    skip_prepare_e2e: bool = False
    force: bool = False
    evg_host_name: Optional[str] = None  # default: branch_dir
    # EVG spawn overrides. None → evg spawn's own defaults
    # (ubuntu2204-latest-large / eu-west-1).
    distro: Optional[str] = None
    region: Optional[str] = None
    # Local-kind mode: spin a kind cluster directly on the laptop instead of
    # provisioning an EVG host. cluster_name defaults to branch_dir.
    local_kind: bool = False
    cluster_name: Optional[str] = None

    def resolved_evg_host_name(self) -> str:
        return self.evg_host_name or self.branch_dir

    def resolved_cluster_name(self) -> str:
        # `kind` is the legacy default cluster name; for local-kind we use
        # the branch_dir so multiple parallel local-kind worktrees don't
        # collide on the same kind cluster.
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
        }

    def worktree_path_or_main(self) -> Path:
        """Where the orchestrator state file lives.

        Always the target worktree dir — Phase 2 keeps it simple: we mkdir
        it ahead of create_worktree.sh, which tolerates an existing empty
        directory. After teardown the .generated/wt-ctl/ inside it goes
        with the worktree.
        """
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
        # Sanity: refuse if any phase is in_progress and the user didn't ask
        # to resume / restart-from. That means a previous run was killed
        # uncleanly — picking up requires explicit acknowledgement.
        if not (resume or restart_from):
            stuck = state.has_in_progress() if state is not None else None
            if stuck:
                raise StateConflict(
                    f"phase '{stuck}' was left in_progress by a previous run; "
                    "re-run with --resume or --restart-from <phase>"
                )

        if state is None:
            # First-ever invocation. Build a fresh state object from the
            # canonical phase order — but DON'T save it yet. Saving creates
            # .generated/wt-ctl/state.json under the target worktree path,
            # which `git worktree add` would then refuse to populate. We
            # persist for the first time after worktree_init has run.
            state = ostate.OrchestratorState.initial(
                branch=self.inputs.branch,
                inputs=self.inputs.to_dict(),
            )
        else:
            # Resume / restart: keep the existing state, but update inputs
            # so future hash comparisons are against the *current* command.
            state.inputs = self.inputs.to_dict()
            state.branch = self.inputs.branch

        phases = self._phases()
        # Resolve which phases to actually execute this run.
        plan = self._plan(state, phases, resume=resume, restart_from=restart_from)
        if not plan:
            emit("[wt-ctl] create: nothing to do (all phases ok).")
            return

        # Run the plan.
        i = 0
        while i < len(plan):
            phase = plan[i]
            # Detect a parallel-pair start: when both PARALLEL_GROUP members
            # are next to each other, run them concurrently.
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
            # Already inside the target worktree? Skip create_worktree.sh —
            # but still apply --context if requested (it's a worktree-init-
            # adjacent side effect per the plan).
            already_inside = _is_same_path(self.runner_cwd_or_main(), wt) and wt.exists()
            if not already_inside:
                host = i.host_worktree_root
                argv = [
                    str(host / "scripts" / "dev" / "create_worktree.sh"),
                ]
                if i.force:
                    argv.append("-f")
                argv.append(i.branch)
                # create_worktree.sh expects PROJECT_DIR to be the dir under
                # which siblings live — that's the calling worktree, matching
                # wt_setup.sh's convention. Falling back to main_repo_root
                # only when we have no worktree context.
                env = {"PROJECT_DIR": str(host)}
                # The log lives in the *host* worktree's logs dir — git
                # worktree add refuses to populate a non-empty target dir,
                # and run_streaming auto-creates log_path.parent. We name
                # the log per-target so multiple parallel `wt-ctl create`s
                # don't collide.
                host_log = logs_dir(host) / f"worktree_init-{i.branch_dir}.log"
                self.runner.run_streaming(
                    argv,
                    prefix="[worktree] ",
                    log_path=host_log,
                    env=env,
                    cwd=host,
                )
            # --context CTX is injected here (between worktree_init and
            # initialize_hook per the plan) so the new worktree is on the
            # right context before .devcontainer/scripts/initialize.sh
            # renders compose.generated.yml.
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
                # A previous run pinned the prefix. The four derived vars
                # (NET_X / NET_Y_BASE / NET_Y_VIP / PROXY_PORT) must also be
                # present — without them docker-compose falls back to the
                # ``${VAR:-default}`` baked into compose.yml (X=16, Y_BASE=0)
                # and clobbers other worktrees' subnets. Catch and repair
                # this case in-place rather than re-allocating, so we stay
                # on the index that the registry still owns for us.
                missing_derived = _missing_derived_env_keys(env_file)
                if missing_derived:
                    params = stack_params(existing_prefix)
                    # ``env_lines_for`` returns all 5 lines — filter to just
                    # the ones the file is missing, in DERIVED_ENV_KEYS order.
                    all_lines = env_lines_for(params)
                    key_to_line = {ln.split("=", 1)[0]: ln for ln in all_lines}
                    repair_lines = [
                        key_to_line[key] for key in DERIVED_ENV_KEYS if key in missing_derived and key in key_to_line
                    ]
                    if repair_lines:
                        with env_file.open("a") as fh:
                            # Make sure we're on a clean newline before
                            # appending so we never merge with a prior key.
                            cur = env_file.read_text()
                            if cur and not cur.endswith("\n"):
                                fh.write("\n")
                            fh.write("\n".join(repair_lines) + "\n")
                return
            block = NetworkDomain(self.runner, wt).allocate(i.branch_dir)
            if not block.startswith("MCK_DEVC_NET_PREFIX="):
                raise WtCtlError(f"NetworkDomain.allocate produced unexpected output: {block!r}")
            # Append all 5 lines (MCK_DEVC_NET_PREFIX + 4 derived vars)
            # to .devcontainer/.env, preserving any other keys.
            env_file.parent.mkdir(parents=True, exist_ok=True)
            with env_file.open("a") as fh:
                fh.write(block)

        # ---- evg_prepare --------------------------------------------------
        def _do_evg_prepare() -> None:
            if i.local_kind:
                # Local-kind mode: kind cluster on this laptop, no EVG host.
                # Sequence (was the body of the now-removed
                # local_kind_prepare.sh):
                #   1. drop any stale EVG-host pin so root-context's
                #      EVG_HOST_NAME resolution doesn't bring in a defunct
                #      host's identity.
                #   2. recreate_kind_cluster.sh writes
                #      .generated/current.kubeconfig directly + uses
                #      DELETE_KIND_NETWORK=false to spare sibling worktrees
                #      sharing the same kind docker network.
                #   3. make switch regenerates context env so CLUSTER_NAME
                #      (derived by root-context from the kubeconfig's
                #      current-context) is reflected in context.env.
                generated = wt / ".generated"
                generated.mkdir(parents=True, exist_ok=True)
                for f in (".current-evg-host", ".current-evg-host-address"):
                    p = generated / f
                    if p.is_file():
                        p.unlink()
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
                # Re-derive context env now that the kubeconfig's
                # current-context is current.
                current_ctx_file = generated / ".current_context"
                if current_ctx_file.is_file():
                    ctx = current_ctx_file.read_text().strip()
                    self.runner.run_streaming(
                        ["make", "switch", f"context={ctx}"],
                        prefix="[local-kind] ",
                        log_path=log_dir / "evg_prepare.log",
                        cwd=wt,
                    )
                # On-host kfp registration (devc-side registration happens
                # later in the orchestrator's `kubeconfig` phase). Call the
                # KubeconfigDomain directly — keeps the python path in-
                # process and skips a subprocess hop. We still need
                # CLUSTER_NAME / K8S_FWD_PROXY / MCK_DEVC_* in env, so
                # read them from the just-regenerated context env files.
                env_for_refresh = _load_context_env(wt)
                from .domains.kubeconfig import KubeconfigDomain  # local import: avoids top-of-module cycle

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
            # Phase semantics: when dc_up runs (not hash-skipped), the world
            # may have shifted under us — fresh devc image from dc_build, a
            # new sibling-service image (gost-proxy / k8s-proxy / autossh),
            # compose.yml / compose.generated.yml drift, a stale operator
            # process inside the running devc, etc. The only way to guarantee
            # a fresh stack is to `compose down` the whole project first,
            # then let `devcontainer up` rebuild it against current state.
            #
            # `devcontainer up` alone is no-op when a container already
            # exists for this workspace — `--remove-existing-container` only
            # tears down the devcontainer service + its `depends_on` chain
            # (k8s-proxy), leaving siblings like gost-proxy on stale config.
            # Project-wide `compose down` sidesteps that asymmetry.
            project = f"{i.branch_dir.lower()}_devcontainer"
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

        # ---- reconcile ----------------------------------------------------
        def _do_reconcile() -> None:
            user_yml = wt / ".devcontainer" / "compose.user.yml"
            if not user_yml.is_file() or user_yml.stat().st_size == 0:
                return  # no overrides → nothing to reconcile
            services = _services_in(user_yml)
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
            # `bash -lc` is a non-interactive login shell. shell-init.sh
            # only auto-sources scripts/dev/devenv for interactive shells
            # (`[[ $- == *i* ]]`), so we explicitly source devenv to export
            # PROJECT_DIR / KUBECONFIG before invoking make. site-context
            # picks KUBECONFIG by side alone (no EVG_HOST_NAME dependency),
            # so make switch produces the right context.devc.env without
            # any pre-export dance from the caller. refresh_kubeconfig.sh
            # is the host-agnostic per-side patcher — works for both
            # EVG-host (assumes evg_host.sh scp already populated
            # .generated/current.kubeconfig) and local-kind (where
            # local_kind_prepare.sh wrote it) modes.
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
            # `bin/reset` (called by prepare-local-e2e) issues its first
            # kubectl request without buffering and can race with k8s-proxy
            # / autossh stabilisation right after `wt-ctl up`. A 503 there
            # aborts the whole phase even though the apiserver is fine
            # seconds later. Poll readyz from Python until it returns 200
            # before kicking off make.
            self._wait_apiserver_ready(
                wt,
                deadline_s=60.0,
                interval_s=2.0,
            )
            # Same devenv-source rationale as the kubeconfig phase:
            # `make prepare-local-e2e` shells out to scripts that expect
            # KUBECONFIG / PROJECT_DIR / context.env to be already exported.
            self.runner.run_streaming(
                _devc_bash(
                    wt,
                    "set -Eeou pipefail; " "cd /workspace; " ". scripts/dev/devenv; " "make prepare-local-e2e",
                ),
                prefix="[prepare-e2e] ",
                log_path=log_dir / "prepare_local_e2e.log",
                cwd=wt,
            )

        # ---- op_run ------------------------------------------------------
        def _do_op_run() -> None:
            # `dc_up`'s compose-down step kills any operator that was
            # running inside the previous devcontainer-1, and `create`
            # itself never re-starts one. The next time the tmuxp layout
            # comes up, the operator pane tails an empty log and the
            # reflectors of any externally-running operator are pinned to
            # the previous kind apiserver port. Launch op_run.sh in detach
            # mode at the tail of create so we always end on "operator is
            # up against the current kubeconfig".
            #
            # Then drop the existing `mck` tmuxp session (if any). When the
            # devc was rebuilt the tmux server inside got fresh too, but a
            # leftover layout from a previous attach can still be cached
            # in zsh's command-hash (e.g. "command not found: python" lingering
            # from a window when venv was mid-recreate) or carry stale
            # kubeconfig handles in k9s. Killing the session forces the next
            # `wt-ctl attach` (no-args) to re-load tmuxp from scratch against
            # the now-current state.
            #
            # Both calls are best-effort: `--detach` is unknown on older
            # op_run.sh variants (descendant branches), and `tmux` may not
            # be present at all in some images. Soft-fail so `create` still
            # finishes.
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
                # net_allocate's idempotency key includes the env-file's
                # actual completeness — if a prior run wrote only
                # MCK_DEVC_NET_PREFIX (the first line of the 5-line block)
                # but crashed before the derived NET_X / NET_Y_BASE / Y_VIP /
                # PROXY_PORT lines, resume must re-enter the phase so the
                # repair branch can append the missing lines. Hashing only
                # by branch_dir would keep the phase ``ok`` and let
                # docker-compose's ``${VAR:-default}`` fall back to
                # 172.16.0.0/23 — colliding with sibling worktrees.
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
                # Always run when the devc-side pipeline is in play. The
                # hash is intentionally time-based so the state.json skip
                # never short-circuits this phase: any time `wt-ctl create`
                # reaches the tail of the plan, the operator must be
                # restarted against the now-current kubeconfig and the
                # tmuxp layout must be re-wired — even on a no-op resume,
                # because dc_up's compose-down already nuked the previous
                # operator and tmux server. op_run.sh is idempotent (kills
                # the existing mck-operator session before relaunching).
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
        """Decide which phases to actually run this invocation.

        Rules:
        * ``--restart-from <phase>`` clears that phase + later from state and
          runs every non-skipped phase from there.
        * ``--resume`` runs from the first phase whose recorded status is
          not OK or whose input_hash no longer matches the current inputs.
          Skipped phases (``Phase.skip()``) are passed over silently.
        * Default: run every non-skipped phase, recording state. This always
          re-runs each phase, which is fine — every phase is idempotent. If
          the user wants to avoid re-running them, they should pass
          --resume (which the in-progress check above lets them do safely
          after a successful run too: first_non_ok will return None).
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
        # We only write state once the host directory exists. Pre-worktree_init
        # the target dir doesn't exist; writing .generated/wt-ctl/ would
        # prevent git worktree add from populating it. Save lazily.
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
        """Runs two phases in two worker threads. Each phase is recorded
        individually (as the plan requires).
        """
        emit(f"[wt-ctl] parallel phases: {a.name} + {b.name}")
        host = self.inputs.worktree_path_or_main()
        for ph in (a, b):
            state.set_status(ph.name, ostate.IN_PROGRESS, log=ph.log_relpath)
        if host.is_dir():
            ostate.save(host, state)

        # Use threads directly (rather than Runner.run_parallel) so we can
        # capture per-phase failures and update state for each. The plan
        # explicitly asks for both to *finish* before we surface failures —
        # ParallelPhaseFailures aggregates exactly that.
        results: dict[str, Optional[Exception]] = {a.name: None, b.name: None}

        import threading

        def _worker(phase: Phase) -> None:
            try:
                phase.run()
            except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
                results[phase.name] = exc

        ta = threading.Thread(target=_worker, args=(a,))
        tb = threading.Thread(target=_worker, args=(b,))
        ta.start()
        tb.start()
        ta.join()
        tb.join()

        # Record outcomes individually.
        for ph in (a, b):
            err = results[ph.name]
            if err is None:
                state.set_status(
                    ph.name,
                    ostate.OK,
                    input_hash=ph.input_hash(),
                    log=ph.log_relpath,
                )
            else:
                state.set_status(
                    ph.name,
                    ostate.FAILED,
                    input_hash=ph.input_hash(),
                    log=ph.log_relpath,
                )
        if host.is_dir():
            ostate.save(host, state)

        failures = {name: err for name, err in results.items() if err is not None}
        if failures:
            # Aggregate as ExternalCommandFailed where possible; otherwise
            # raise the first exception. Any non-ECF exceptions get re-raised
            # as the first failure.
            ecfs = {name: err for name, err in failures.items() if isinstance(err, ExternalCommandFailed)}
            if ecfs:
                raise ParallelPhaseFailures(failures=ecfs)
            # Single non-ECF failure: just re-raise it.
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
        # Don't mkdir the worktree path here — git worktree add refuses to
        # add into a non-empty directory. We let save() create
        # .generated/wt-ctl/ on demand once worktree_init has populated
        # the worktree dir.
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
        """Poll ``kubectl get --raw=/readyz`` inside the devcontainer until
        it returns 200 or the deadline elapses. Raises ``WtCtlError`` on
        timeout. Probes are non-fatal — a failed probe (rc != 0) just
        triggers another sleep.

        Probe output goes only to stderr; the surrounding phase's
        ``run_streaming`` writes the eventual ``make prepare-local-e2e``
        output to the phase log so we don't double-log.
        """
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
            # Emit a liveness tick every 5 probes so the user sees
            # progress during a slow handshake instead of silence.
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


# ---------------------------------------------------------------------------
# DeleteOrchestrator
# ---------------------------------------------------------------------------


@dataclass
class DeleteInputs:
    """Opt-in delete targets. Every flag defaults False — ``wt-ctl delete``
    with no flags is a deliberate no-op (the CLI layer prints help). Git
    branches are NEVER deleted; the branch field is metadata only.
    """

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
    failures inside a step log and continue (other targets still run).
    Branch deletion was removed deliberately — recreating worktrees is
    cheap, recreating lost work isn't.
    """

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
        project = f"{self.inputs.branch_dir.lower()}_devcontainer"
        # Ask docker compose if anything's there before we down.
        ps = self.runner.run(
            ["docker", "compose", "-p", project, "ps", "--format", "{{.Name}}"],
            check=False,
        )
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
        """Return the kubeconfig's top-level ``current-context`` field, or
        None if the file is unreadable / malformed / unset.

        Lazy yaml import: ``wt-ctl delete`` should still work when pyyaml
        isn't installed (e.g. very early worktree state); we'd just have to
        skip the kind-teardown step in that case.
        """
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
        # No `.current-evg-host` pinned? Then this worktree is either
        # local-kind (we created a kind cluster on this laptop) or BYOC
        # (user owns whatever cluster the kubeconfig points at). Read the
        # kubeconfig's current-context; if it starts with `kind-` AND that
        # cluster exists in `kind get clusters`, delete it. Otherwise
        # no-op — BYOC tears itself down.
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
        # Ask evergreen for our hosts and find the matching displayName.
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
        # Pre-emptively move our cwd off the worktree we're about to delete.
        # If wt-ctl was invoked from inside the worktree (the canonical
        # case), the directory we're sitting in is about to vanish — and
        # any later `subprocess.Popen` / `os.getcwd()` then raises
        # FileNotFoundError on Linux/macOS. Hop to the worktree's parent;
        # it always exists.
        try:
            cwd = os.getcwd()
            if Path(cwd).resolve() == wt.resolve() or wt in Path(cwd).resolve().parents:
                os.chdir(wt.parent)
        except (FileNotFoundError, OSError):
            # Cwd is already gone (rare race). Hop to a guaranteed-safe spot.
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
        # prune is always best-effort.
        try:
            self.runner.run(
                ["git", "-C", str(self.inputs.main_repo_root), "worktree", "prune"],
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] worktree: prune failed (continuing): {exc.render()}")

    def _step_prefix_release(self, emit: Callable[[str], None]) -> None:
        emit(f"[wt-ctl] network: releasing registry entry for '{self.inputs.branch_dir}'")
        # Native Registry.release() — no shim hop, no path-resolution
        # gymnastics across deleted worktrees.
        try:
            # release() doesn't consult worktree_parent — any sensible
            # repo_root works. main_repo_root is canonical and always set.
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
    """Return ``DERIVED_ENV_KEYS`` entries absent from ``env_file``.

    Used by ``net_allocate`` resume to recover from a partial first-write
    that landed only ``MCK_DEVC_NET_PREFIX=<N>`` — a state which would
    otherwise let compose.yml's default ``X=16, Y_BASE=0`` collide with
    other worktrees' /23 subnets.
    """
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
    """Tiny YAML extraction: collect top-level service keys under ``services:``.

    We deliberately avoid PyYAML (stdlib only). compose.user.yml is hand-
    edited by humans, so the structure is consistently 2-space-indented YAML.
    """
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
