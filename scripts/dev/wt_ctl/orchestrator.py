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
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Iterable, Optional

from . import orchestrator_state as ostate
from .domains.compose import project_name_for
from .errors import (
    ExternalCommandFailed,
    ParallelPhaseFailures,
    StateConflict,
    ToolMissing,
    WtCtlError,
)
from .paths import logs_dir
from .runner import Runner


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
    worktree_path: Path           # target worktree root (may not yet exist)
    main_repo_root: Path           # the main repo (parent of .git/common dir)
    host_worktree_root: Path       # the worktree the user invoked from; used
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

    def resolved_evg_host_name(self) -> str:
        return self.evg_host_name or self.branch_dir

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
        }

    def worktree_path_or_main(self) -> Path:
        """Where the orchestrator state file lives.

        Always the target worktree dir — Phase 2 keeps it simple: we mkdir
        it ahead of create_worktree.sh, which tolerates an existing empty
        directory. After teardown the .wt-ctl/ inside it goes with the
        worktree.
        """
        return self.worktree_path


# ---------------------------------------------------------------------------
# Phase
# ---------------------------------------------------------------------------

@dataclass
class Phase:
    name: str
    run: Callable[[], None]                                 # idempotent action
    input_hash: Callable[[], str]                           # hash inputs
    skip: Callable[[], bool] = lambda: False                 # static skip (e.g. --skip-evg)
    log_relpath: Optional[str] = None                       # for state.log


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
            # .wt-ctl/state.json under the target worktree path, which
            # `git worktree add` would then refuse to populate. We persist
            # for the first time after worktree_init has run.
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
                host_log = (
                    logs_dir(host) / f"worktree_init-{i.branch_dir}.log"
                )
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
                return
            res = self.runner.run(
                [
                    str(wt / "scripts" / "dev" / "dc_select_network.sh"),
                    "--branch-dir", i.branch_dir,
                ],
                cwd=wt,
            )
            line = (res.stdout or "").strip()
            if not line.startswith("MCK_DEVC_NET_PREFIX="):
                raise WtCtlError(
                    f"dc_select_network.sh produced unexpected output: {line!r}"
                )
            # Append to .devcontainer/.env (preserves any other keys).
            env_file.parent.mkdir(parents=True, exist_ok=True)
            with env_file.open("a") as fh:
                fh.write(line + "\n")

        # ---- evg_prepare --------------------------------------------------
        def _do_evg_prepare() -> None:
            argv = [
                str(wt / "scripts" / "dev" / "evg_prepare.sh"),
                "--name", i.resolved_evg_host_name(),
            ]
            if i.multi_cluster:
                argv.append("--multi")
            if i.skip_recreate:
                argv.append("--skip-recreate")
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
                [
                    "devcontainer", "exec",
                    "--workspace-folder", str(wt),
                    "bash", "-lc", cmd,
                ],
                prefix="[post-start] ",
                log_path=log_dir / "post_start.log",
                cwd=wt,
            )

        # ---- kubeconfig ---------------------------------------------------
        def _do_kubeconfig() -> None:
            cmd = (
                "set -Eeou pipefail; "
                "cd /workspace; "
                'make switch context="$(cat .generated/.current_context)"; '
                "bash scripts/dev/evg_host.sh get-kubeconfig --no-fetch"
            )
            self.runner.run_streaming(
                [
                    "devcontainer", "exec",
                    "--workspace-folder", str(wt),
                    "bash", "-lc", cmd,
                ],
                prefix="[refresh] ",
                log_path=log_dir / "refresh_kubeconfig.log",
                cwd=wt,
            )

        # ---- prepare_e2e --------------------------------------------------
        def _do_prepare_e2e() -> None:
            cmd = "set -Eeou pipefail; cd /workspace && make prepare-local-e2e"
            self.runner.run_streaming(
                [
                    "devcontainer", "exec",
                    "--workspace-folder", str(wt),
                    "bash", "-lc", cmd,
                ],
                prefix="[prepare-e2e] ",
                log_path=log_dir / "prepare_local_e2e.log",
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
                name="net_allocate",
                run=_do_net_allocate,
                input_hash=lambda: _h({"branch_dir": i.branch_dir}),
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
        # the target dir doesn't exist; writing .wt-ctl/ would prevent
        # git worktree add from populating it. Save lazily.
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
                    ph.name, ostate.OK,
                    input_hash=ph.input_hash(),
                    log=ph.log_relpath,
                )
            else:
                state.set_status(
                    ph.name, ostate.FAILED,
                    input_hash=ph.input_hash(),
                    log=ph.log_relpath,
                )
        if host.is_dir():
            ostate.save(host, state)

        failures = {
            name: err for name, err in results.items() if err is not None
        }
        if failures:
            # Aggregate as ExternalCommandFailed where possible; otherwise
            # raise the first exception. Any non-ECF exceptions get re-raised
            # as the first failure.
            ecfs = {
                name: err for name, err in failures.items()
                if isinstance(err, ExternalCommandFailed)
            }
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
        # add into a non-empty directory. We let save() create .wt-ctl/ on
        # demand once worktree_init has populated the worktree dir.
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


# ---------------------------------------------------------------------------
# DeleteOrchestrator
# ---------------------------------------------------------------------------

@dataclass
class DeleteInputs:
    branch: str
    branch_dir: str
    worktree_path: Path
    main_repo_root: Path
    delete_branch: bool = False
    keep_evg: bool = False
    keep_worktree: bool = False
    keep_stack: bool = False
    keep_om_projects: bool = False
    evg_host_name: Optional[str] = None

    def resolved_evg_host_name(self) -> str:
        return self.evg_host_name or self.branch_dir


class DeleteOrchestrator:
    """Mirrors ``wt_teardown.sh``. Each step is best-effort; failures log
    and the next step still runs. No state file is written.
    """

    def __init__(self, runner: Runner, inputs: DeleteInputs) -> None:
        self.runner = runner
        self.inputs = inputs
        self.scripts = inputs.main_repo_root / "scripts" / "dev"

    def run(self, *, emit: Callable[[str], None] = lambda _msg: None) -> None:
        emit(
            f"[wt-ctl] delete: branch={self.inputs.branch} "
            f"worktree={self.inputs.worktree_path} "
            f"evg_host={self.inputs.resolved_evg_host_name()}"
        )
        # Each step catches its own typed errors and logs; the orchestrator
        # never re-raises until everything has run.
        self._step_om_clean(emit)
        self._step_compose_down(emit)
        self._step_evg_terminate(emit)
        self._step_worktree_remove(emit)
        self._step_prefix_release(emit)
        if self.inputs.delete_branch:
            self._step_branch_delete(emit)
        emit("[wt-ctl] delete: done")

    # ------------------------------------------------------------------
    def _step_om_clean(self, emit: Callable[[str], None]) -> None:
        if self.inputs.keep_om_projects:
            emit("[wt-ctl] om: skipped (--keep-om-projects)")
            return
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
        if self.inputs.keep_stack:
            emit("[wt-ctl] compose: skipped (--keep-stack)")
            return
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

    def _step_evg_terminate(self, emit: Callable[[str], None]) -> None:
        if self.inputs.keep_evg:
            emit("[wt-ctl] evg: skipped (--keep-evg)")
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
            res.stdout or "[]", self.inputs.resolved_evg_host_name(),
        )
        if host_id is None:
            emit(
                f"[wt-ctl] evg: no running host with displayName "
                f"'{self.inputs.resolved_evg_host_name()}'"
            )
            return
        emit(
            f"[wt-ctl] evg: terminating "
            f"'{self.inputs.resolved_evg_host_name()}' (host_id={host_id})"
        )
        try:
            self.runner.run_streaming(
                ["evergreen", "host", "terminate", "--host", host_id],
                prefix="[evg-term] ",
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] evg: terminate failed (continuing): {exc.render()}")

    def _step_worktree_remove(self, emit: Callable[[str], None]) -> None:
        if self.inputs.keep_worktree:
            emit("[wt-ctl] worktree: skipped (--keep-worktree)")
            return
        wt = self.inputs.worktree_path
        if wt.is_dir():
            emit(f"[wt-ctl] worktree: removing {wt}")
            try:
                self.runner.run(
                    [
                        "git", "-C", str(self.inputs.main_repo_root),
                        "worktree", "remove", "--force", str(wt),
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
        # The target worktree's scripts have already been removed by the
        # preceding _step_worktree_remove. main_repo_root points at the bare
        # `.git` parent which typically has no working tree, so fall back
        # candidates in order: target worktree (still there if --keep-worktree),
        # main_repo_root, then the cwd (the user invoked wt-ctl from
        # somewhere — usually a sibling worktree which has the script).
        candidates = [
            self.inputs.worktree_path / "scripts" / "dev" / "dc_select_network.sh",
            self.scripts / "dc_select_network.sh",
            Path(os.getcwd()) / "scripts" / "dev" / "dc_select_network.sh",
        ]
        script = next((c for c in candidates if c.is_file()), candidates[0])
        try:
            self.runner.run_streaming(
                [str(script), "--release", self.inputs.branch_dir],
                prefix="[net-release] ",
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] network: release failed (continuing): {exc.render()}")

    def _step_branch_delete(self, emit: Callable[[str], None]) -> None:
        # Verify the branch exists locally first.
        check = self.runner.run(
            [
                "git", "-C", str(self.inputs.main_repo_root),
                "show-ref", "--verify", "--quiet",
                f"refs/heads/{self.inputs.branch}",
            ],
            check=False,
        )
        if check.rc != 0:
            emit(f"[wt-ctl] branch: '{self.inputs.branch}' doesn't exist locally")
            return
        emit(f"[wt-ctl] branch: deleting '{self.inputs.branch}'")
        try:
            self.runner.run(
                [
                    "git", "-C", str(self.inputs.main_repo_root),
                    "branch", "-D", self.inputs.branch,
                ],
            )
        except (ExternalCommandFailed, ToolMissing, WtCtlError) as exc:
            emit(f"[wt-ctl] branch: delete failed (continuing): {exc.render()}")


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


