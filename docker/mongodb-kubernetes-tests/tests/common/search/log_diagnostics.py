"""Reusable log-gather + cursor-tree rendering for RS search e2e tests.

Drop-in: wrap the body of any single-cluster RS test that perturbs the
search data path (envoy kill, mongot delete, network outage, etc.) in
``capture_rs_cursor_view(...)``. On exit, the helper fetches logs from
every layer (envoy, mongot, mongod), feeds them through the
log_analyzer pipeline that the CLI uses, and emits the per-cursor
box-drawing tree + unified timeline through ``test_logger`` — same
output format as ``python -m tests.common.search.log_analyzer.cli``.

Why a context manager and not a fixture:
  - Tests want the view on BOTH success and failure paths. ``try/finally``
    in the test body is fine but verbose to copy-paste; a context manager
    wraps it cleanly.
  - The mongod debug-verbosity bump must surround the workload, not the
    fixture setup phase. Coupling it to the manager means the bump is
    consistently scoped + restored, even if the test raises mid-body.

Single-cluster RS only for now. Sharded uses ``_build_sharded_trees`` +
``render_sharded_cursor_trees`` from the analyzer; the equivalent helper
for sharded can be added once a sharded test wants it.
"""

from __future__ import annotations

import contextlib
import io
import os
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

import requests
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.log_analyzer import collector as log_collector
from tests.common.search.log_analyzer.analyzer import print_cursor_trees, print_unified_timeline, set_mongod_debug_logs
from tests.common.search.log_analyzer.cli import _build_rs_trees

logger = test_logger.get_test_logger(__name__)


_ENVOY_DEBUG_PATHS = "http2:debug,http:debug,router:debug,upstream:debug,connection:debug"
_ENVOY_INFO_PATHS = "http2:info,http:info,router:info,upstream:info,connection:info"
_ENVOY_ADMIN_PORT = 9901


def set_envoy_log_level(
    namespace: str,
    mdbs_name: str,
    *,
    paths: str = _ENVOY_DEBUG_PATHS,
    replica_hits: int = 4,
) -> bool:
    """POST ``/logging?paths=<paths>`` to envoy admin via the proxy Service.

    The operator's search-lb Service exposes port 9901 named ``admin``
    that targets each envoy pod's admin endpoint. ``*.svc.cluster.local``
    resolves from inside the devcontainer through the k8s-proxy chain,
    so this is a plain HTTP POST — no per-pod IP routing or HTTP-proxy
    plumbing needed. ``/logging`` is whitelisted in envoy's admin
    AllowPaths (see ``envoy_config_builder.go``).

    Without this, ``parse_envoy_debug_log`` returns empty — envoy's
    per-frame http2/http logs are gated behind the runtime ``/logging``
    knob.

    ``replica_hits`` — best-effort: POST N times so the k8s Service
    round-robin amortizes across pods if envoy has >1 replica. Default
    of 4 covers single-replica deterministically and 2-replica with
    >93% per-pod hit probability.
    """
    proxy_svc = search_resource_names.proxy_service_name(mdbs_name)
    fqdn = f"{proxy_svc}.{namespace}.svc.cluster.local"
    url = f"http://{fqdn}:{_ENVOY_ADMIN_PORT}/logging"
    successes = 0
    for i in range(max(1, replica_hits)):
        try:
            resp = requests.post(url, params={"paths": paths}, timeout=10)
            resp.raise_for_status()
            successes += 1
        except Exception as exc:
            logger.info(f"envoy /logging POST {i + 1}/{replica_hits} failed: {exc!r}")
    logger.info(f"envoy /logging paths={paths} via {fqdn}:9901 " f"({successes}/{replica_hits} POSTs succeeded)")
    return successes > 0


def discover_rs_search_pods(
    namespace: str,
    *,
    mdb_name: str,
    mdbs_name: str,
    rs_members: int,
) -> dict[str, list[str]]:
    """Return ``{"envoy": [...], "mongot": [...], "mongod": [...]}`` for the RS topology.

    Mongot discovery filters to ordinal-suffixed pods so the LB
    Deployment pods don't sneak in via prefix collision
    (``<mdb>-search-`` is also a prefix of ``<mdb>-search-lb-...``).
    """
    envoy_dep = search_resource_names.lb_deployment_name(mdbs_name)
    mongot_sts = search_resource_names.mongot_statefulset_name(mdbs_name)

    envoy_pods = log_collector.discover_pods(namespace, label_selector=f"app={envoy_dep}")
    mongot_pods_raw = log_collector.discover_pods(namespace, name_prefix=mongot_sts + "-")
    mongot_pods = [p for p in mongot_pods_raw if p[p.rfind("-") + 1 :].isdigit()]
    mongod_pods = [f"{mdb_name}-{i}" for i in range(rs_members)]
    return {"envoy": envoy_pods, "mongot": mongot_pods, "mongod": mongod_pods}


def render_rs_cursor_view(
    *,
    trees: list,
    timeline: list,
    header: str,
    input_counts: Optional[dict[str, int]] = None,
) -> None:
    """Log already-built trees + timeline as box-drawing output via test_logger."""
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        print_cursor_trees(trees)
        print_unified_timeline(timeline, max_events=0, color=False)
    rendered = buf.getvalue().strip("\n")
    if rendered:
        logger.info("%s:\n%s", header, rendered)
    else:
        sizes = f" input sizes: {input_counts}" if input_counts else ""
        logger.info(f"{header}: empty — trees={len(trees)} timeline_events={len(timeline)};{sizes}")


class capture_rs_cursor_view:
    """Context manager: bump verbosity, snapshot logs, render cursor view, expose state.

    Usage::

        with capture_rs_cursor_view(
            mdb=mdb, mdbs=mdbs, namespace=namespace,
            admin_client=admin_tester.client, rs_members=cfg.rs_members,
        ) as capture:
            # ... baseline work ...
            capture.snapshot("pre-fault")     # optional: catches logs of about-to-die pods
            # ... fault + post-fault work ...

        # After the with block, the helper has populated:
        assert len(capture.trees) == 1
        assert any(t.contains_cursor_lost() for t in capture.trees)
        assert capture.timeline                # cross-layer events
        capture.store                          # raw LogStore for ad-hoc SQL

    On enter:
      - Discovers envoy/mongot/mongod pods (envoy pre-perturbation).
      - Bumps mongod COMMAND+NETWORK verbosity to 2 via ``admin_client``.
      - POSTs envoy ``/logging?paths=…:debug`` so the http2/http frame
        records hit stdout (gated by envoy admin AllowPaths — must
        include ``/logging``, see envoy_config_builder.go).
      - Creates a single ``capture_dir`` (override via the constructor
        kwarg). All snapshots write subdirs underneath it so the whole
        run lands in one tree — point lnav at the dir for an
        interleaved cross-layer view.
      - Records a wall-clock marker that anchors ``since_seconds``.

    During the workload the test can call ``snapshot(label)`` at any
    point to fetch current logs of all envoy/mongot/mongod pods. The
    most important time to call it is **just before destructive work**
    (e.g. ``hard_kill_pods_by_label``) so the pod's logs are captured
    while the pod object still exists in the API — once
    ``delete_namespaced_pod`` returns, the logs are gone with the pod.

    On exit (success OR exception):
      - Takes a final snapshot.
      - Concatenates log paths across all snapshots → builds trees +
        unified timeline via the same code path as the CLI.
      - Renders both through ``test_logger`` as one multi-line message.
      - Exposes ``self.trees`` / ``self.timeline`` / ``self.store`` for
        post-with assertions.
      - Restores mongod verbosity and envoy log level (best-effort).
    """

    def __init__(
        self,
        *,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        namespace: str,
        admin_client: Any,
        rs_members: int,
        header: str = "cursor view (trees + timeline)",
        enable_envoy_debug: bool = True,
        focus_cursor_id: Optional[int] = None,
        capture_dir: Optional[str] = None,
    ) -> None:
        self._mdb = mdb
        self._mdbs = mdbs
        self._namespace = namespace
        self._admin_client = admin_client
        self._rs_members = rs_members
        self._header = header
        self._enable_envoy_debug = enable_envoy_debug
        self._focus_cursor_id = focus_cursor_id
        # One root capture dir per context-manager instance; every snapshot
        # writes into a labelled subdir underneath. The default is a fresh
        # tempdir created in __enter__, but callers can override (e.g. to
        # park captures under the test's logs/ tree). Either way, lnav can
        # consume the whole run with ``lnav <capture_dir>``.
        self._capture_dir: Optional[Path] = Path(capture_dir) if capture_dir else None
        self._marker: Optional[datetime] = None
        self._mongot_pods: list[str] = []
        self._mongod_pods: list[str] = []
        self._verbosity_bumped = False
        self._envoy_debug_set = False
        # Snapshots are dicts: {"label", "envoy", "mongot", "mongod"} of log file paths.
        self._snapshots: list[dict] = []
        # Populated on __exit__:
        self.trees: list = []
        self.timeline: list = []
        self.store: Any = None

    def focus(self, cursor_id: Optional[int]) -> None:
        """Restrict the rendered cursor tree to this single cursor_id.

        Tests that open one workload cursor of interest can pass it via
        ``focus_cursor_id`` at construction time, or set it later via
        this method once the cursor exists (e.g. after
        ``tool.paging_cursor_open``). When set, ``__exit__`` drops every
        other tree from ``self.trees`` and filters the timeline events
        to those whose ``cursor_id`` matches (events without a
        cursor_id — envoy/mongot frame events keyed on client_id —
        survive so the cross-layer chain remains readable).
        """
        self._focus_cursor_id = cursor_id

    def __enter__(self) -> "capture_rs_cursor_view":
        pods = discover_rs_search_pods(
            self._namespace,
            mdb_name=self._mdb.name,
            mdbs_name=self._mdbs.name,
            rs_members=self._rs_members,
        )
        self._mongot_pods = pods["mongot"]
        self._mongod_pods = pods["mongod"]
        logger.info(
            f"capture_rs_cursor_view: envoy_pods_initial={pods['envoy']} "
            f"mongot_pods={self._mongot_pods} mongod_pods={self._mongod_pods}"
        )

        try:
            set_mongod_debug_logs(self._admin_client, command_level=2, network_level=2)
            self._verbosity_bumped = True
            logger.info("capture_rs_cursor_view: mongod COMMAND+NETWORK verbosity bumped to 2")
        except Exception as exc:
            logger.info(f"capture_rs_cursor_view: failed to bump mongod verbosity (continuing): {exc!r}")

        if self._enable_envoy_debug:
            self._envoy_debug_set = set_envoy_log_level(self._namespace, self._mdbs.name)

        if self._capture_dir is None:
            self._capture_dir = Path(tempfile.mkdtemp(prefix="mck-capture-"))
        else:
            self._capture_dir.mkdir(parents=True, exist_ok=True)
        logger.info(f"capture_rs_cursor_view: capture_dir={self._capture_dir}")
        self._marker = datetime.now(timezone.utc)
        return self

    def snapshot(self, label: str = "snapshot") -> dict:
        """Fetch current envoy/mongot/mongod logs and stash for the final render.

        Call right before destructive operations (pod kill / delete) so
        the to-be-deleted pods' logs are captured while they still
        exist. The render in ``__exit__`` concatenates every snapshot.

        Returns the fetched paths dict so callers can inspect them
        ad-hoc (e.g. ``len(snap["envoy"])`` to confirm a fetch
        happened). Failures are caught and logged; the snapshot is
        recorded with whatever paths succeeded.
        """
        assert self._marker is not None, "snapshot() called before __enter__"
        assert self._capture_dir is not None
        envoy_dep = search_resource_names.lb_deployment_name(self._mdbs.name)
        envoy_pods = log_collector.discover_pods(self._namespace, label_selector=f"app={envoy_dep}")
        since_seconds = int((datetime.now(timezone.utc) - self._marker).total_seconds()) + 5
        # Per-snapshot subdir under the root capture_dir keeps every layer's
        # files together and lets ``_collect_paths`` use basename dedup
        # across snapshots (envoy/mongot have stable per-pod basenames).
        snap_dir = self._capture_dir / f"snap{len(self._snapshots) + 1:02d}-{label}"
        snap_dir.mkdir(parents=True, exist_ok=True)
        try:
            envoy_logs = log_collector.fetch_pod_logs(
                self._namespace, envoy_pods, since_seconds=since_seconds, dest_dir=snap_dir
            )
            mongot_logs = log_collector.fetch_pod_logs(
                self._namespace, self._mongot_pods, since_seconds=since_seconds, dest_dir=snap_dir
            )
            # mongod log: read the on-disk file via kube exec rather than
            # pod stdout. The file lives on the agent log dir's PV so a
            # killed-and-replaced pod still leaves its history visible to
            # the next fetch, which the stdout path can't deliver.
            mongod_logs = log_collector.fetch_database_log_files(self._namespace, self._mongod_pods, dest_dir=snap_dir)
        except Exception as exc:
            logger.info(f"capture_rs_cursor_view: snapshot {label!r} fetch raised: {exc!r}")
            envoy_logs, mongot_logs, mongod_logs = [], [], []
        snap = {
            "label": label,
            "envoy": envoy_logs,
            "mongot": mongot_logs,
            "mongod": mongod_logs,
        }
        self._snapshots.append(snap)
        logger.info(
            f"capture_rs_cursor_view: snapshot {label!r} dir={snap_dir} "
            f"since_seconds={since_seconds} envoy_pods={envoy_pods} "
            f"(fetched envoy={len(envoy_logs)} mongot={len(mongot_logs)} mongod={len(mongod_logs)})"
        )
        return snap

    def refresh_envoy_debug(self) -> None:
        """Re-bump envoy debug logging — call after a replacement envoy pod is Ready.

        Envoy log-level is per-process; a fresh pod boots at the level
        baked into envoy.json (info). Call this after waiting for the
        replacement to be Ready and BEFORE the workload resumes paging,
        otherwise post-fault frames go through at INFO and the cursor
        tree's envoy node ends up empty.
        """
        if not self._enable_envoy_debug:
            return
        ok = set_envoy_log_level(self._namespace, self._mdbs.name)
        self._envoy_debug_set = self._envoy_debug_set or ok

    def _collect_paths(self) -> dict[str, list[str]]:
        """Merge per-pod log paths across snapshots without producing duplicates.

        Each snapshot fetches every pod's full log window. Concatenating
        them verbatim makes the analyzer parse identical events twice
        and inflate the cursor tree / timeline with phantom duplicates.

        Per-layer rules:

        - **envoy / mongot**: dedup by pod basename, walking snapshots
          newest-first. The latest snapshot's path wins (it has the
          deepest log window). Pods that only exist in earlier
          snapshots (killed/replaced) get picked up from there — that's
          the whole point of pre-fault snapshotting.
        - **mongod**: take only the final snapshot's paths.
          ``fetch_database_log_files`` reads the on-disk mongod log
          file via kube exec, so each snapshot already contains the
          full PV-backed history — merging across snapshots would
          just duplicate the same events. The final snapshot has the
          most recent (and complete) view.
        """
        out: dict[str, list[str]] = {"envoy": [], "mongot": [], "mongod": []}
        for layer in ("envoy", "mongot"):
            seen: set[str] = set()
            for snap in reversed(self._snapshots):
                for path in snap[layer]:
                    base = os.path.basename(path)
                    if base in seen:
                        continue
                    seen.add(base)
                    out[layer].append(path)
        if self._snapshots:
            out["mongod"] = list(self._snapshots[-1]["mongod"])
        return out

    def __exit__(self, exc_type, exc_val, exc_tb) -> None:
        try:
            self.snapshot("final")
            paths = self._collect_paths()
            envoy_paths = paths["envoy"]
            mongot_paths = paths["mongot"]
            mongod_paths = paths["mongod"]
            logger.info(
                f"capture_rs_cursor_view: building view across "
                f"{len(self._snapshots)} snapshot(s) — total paths: "
                f"envoy={len(envoy_paths)} mongot={len(mongot_paths)} mongod={len(mongod_paths)}"
            )
            for layer, paths_ in (("envoy", envoy_paths), ("mongot", mongot_paths), ("mongod", mongod_paths)):
                for p in paths_:
                    logger.info(f"capture_rs_cursor_view: parsing {layer} log: {p}")
            try:
                self.trees, self.timeline, self.store = _build_rs_trees(
                    namespace=self._namespace,
                    paths_by_group={
                        "mongod": mongod_paths,
                        "mongot": mongot_paths,
                        "envoy": envoy_paths,
                    },
                )
            except Exception as exc:
                logger.info(f"capture_rs_cursor_view: _build_rs_trees raised: {exc!r}")
                self.trees, self.timeline, self.store = [], [], None

            # Optional focus: drop everything not tied to the cursor we care
            # about. Events without a cursor_id (envoy stream frames keyed
            # on client_id, mongot interceptor records) survive — the
            # cross-layer chain stays readable. NON-strict: if the cursor
            # isn't represented in the trees (analyzer dropped it because
            # the aggregate's mongod COMMAND record was missed by the log
            # capture window — known flaky on kubectl-logs buffering), fall
            # back to ALL trees so the rendered view still tells SOME
            # story. The warning makes the gap visible.
            if self._focus_cursor_id is not None:
                focus = self._focus_cursor_id
                all_trees = list(self.trees)
                kept_trees = [t for t in all_trees if getattr(t, "cursor_id", None) == focus]
                if kept_trees:
                    self.trees = kept_trees
                    self.timeline = [ev for ev in self.timeline if getattr(ev, "cursor_id", None) in (None, focus)]
                    logger.info(
                        f"capture_rs_cursor_view: focus_cursor_id={focus} — kept "
                        f"{len(self.trees)} tree(s), dropped {len(all_trees) - len(self.trees)}, "
                        f"timeline filtered to {len(self.timeline)} event(s)"
                    )
                else:
                    logger.info(
                        f"capture_rs_cursor_view: focus_cursor_id={focus} matched 0 of "
                        f"{len(all_trees)} tree(s) — analyzer didn't build a tree for this "
                        f"cursor (likely the aggregate's mongod COMMAND record fell outside "
                        f"the capture window). Rendering ALL trees unfiltered."
                    )

            render_rs_cursor_view(
                trees=self.trees,
                timeline=self.timeline,
                header=self._header,
                input_counts={
                    "envoy": len(envoy_paths),
                    "mongot": len(mongot_paths),
                    "mongod": len(mongod_paths),
                },
            )
        except Exception as render_exc:
            logger.info(f"capture_rs_cursor_view: gather/render raised (ignored): {render_exc!r}")
        finally:
            if self._verbosity_bumped:
                try:
                    set_mongod_debug_logs(self._admin_client, command_level=0, network_level=0)
                    logger.info("capture_rs_cursor_view: mongod verbosity restored to defaults")
                except Exception as exc:
                    logger.info(f"capture_rs_cursor_view: failed to restore mongod verbosity (ignored): {exc!r}")
            if self._envoy_debug_set:
                set_envoy_log_level(self._namespace, self._mdbs.name, paths=_ENVOY_INFO_PATHS)
        # Don't suppress the test's exception.
        return None
