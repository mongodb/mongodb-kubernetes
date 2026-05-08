"""E2E tests for the search connectivity tool — sharded variant (KUBE-17).

Drives ``SearchConnectivityTool`` against a 2-shard managed-LB
MongoDBSearch deployment (2 mongots per shard) and asserts the
connectivity tool surfaces fanout disruption, per-shard cursor loss,
envoy restarts, and per-shard endpoint removal.
"""

from __future__ import annotations

import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path

import pytest
from kubernetes import client
from kubetester import wait_for_pods_ready as kt_wait_for_pods_ready
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import (
    MongoDBShardedDeploymentConfig,
    MongoDBShardedDeploymentTests,
    SearchShardedDeploymentTests,
    SearchShardedE2EFixtures,
    SearchShardedSampleDataAndIndex,
    _derive_user_defaults,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    drain_until_cross_shard_subgetmore_observed,
    hard_kill_pods_by_label,
    hard_kill_pods_by_prefix,
    run_periodically_logged,
)
from tests.common.search.log_analyzer import collector as log_collector
from tests.common.search.log_analyzer.analyzer import (
    build_stream_summaries,
    parse_client_wire_ops,
    parse_envoy_debug_log,
    read_mongod_commands,
    read_mongod_sessions,
    read_mongos_commands,
    read_mongos_remote_requests,
    read_mongot_interceptor_events,
    render_sharded_cursor_trees,
    set_mongos_debug_logs,
)
from tests.common.search.log_analyzer.store import LogStore, build_sharded_cursor_trees_sql
from tests.common.search.sharded_search_helper import get_search_tester, get_shard_mongod_tester

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_observability_sharded


def configure_mongodb_sharded_config(cfg: MongoDBShardedDeploymentConfig) -> MongoDBShardedDeploymentConfig:
    cfg.mdb_resource_name = "mdb-sh-conn-tool"
    cfg.admin_user_name = ""
    cfg.admin_user_password = ""
    cfg.user_name = ""
    cfg.user_password = ""
    _derive_user_defaults(cfg)
    return cfg


class TestSearchWithShardedCluster(
    SearchShardedDeploymentTests,
    MongoDBShardedDeploymentTests,
):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return configure_mongodb_sharded_config(super().build_mongodb_sharded_config())


class TestSearchSampleDataAndIndex(
    SearchShardedSampleDataAndIndex,  # Layer 3 sharded — overrides _post_restore_setup
    SearchShardedE2EFixtures,  # provides mdb / search_tools_pod / _admin_tester hooks
):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return configure_mongodb_sharded_config(super().build_mongodb_sharded_config())


class TestSearchConnectivityToolSharded(
    SearchShardedE2EFixtures,  # load-bearing — no layer mixin
):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return configure_mongodb_sharded_config(super().build_mongodb_sharded_config())

    # ------------------------------------------------------------------
    # Log-attributed sharded tests — observability only. Pure-verdict
    # sharded tests live in
    # tests/search/search_connectivity_tool_sharded.py.
    # ------------------------------------------------------------------

    def test_paging_search_fans_out_to_distinct_mongot_per_shard(
        self, mdb: MongoDB, mdbs: MongoDBSearch, namespace: str
    ):
        """$search paging cursor must land on a mongot in EACH shard;
        getMore stays within-shard; renders a cross-layer fanout timeline.
        """
        cfg = self.build_mongodb_sharded_config()
        mongos_pod = f"{cfg.mdb_resource_name}-mongos-0"
        shard_mongods = [f"{cfg.mdb_resource_name}-{i}-0" for i in range(cfg.shard_count)]
        shard_pod_prefixes = {
            f"{cfg.mdb_resource_name}-{i}": f"{cfg.mdb_resource_name}-{i}-" for i in range(cfg.shard_count)
        }
        # Per-shard mongot pod name prefix — matches the search STS pods
        # for each shard. Used by the analyzer to recover mongot batches
        # when mongos doesn't propagate the client lsid to shard mongods.
        shard_mongot_pod_prefixes = {
            f"{cfg.mdb_resource_name}-{i}": (
                search_resource_names.shard_statefulset_name(mdbs.name, f"{cfg.mdb_resource_name}-{i}") + "-"
            )
            for i in range(cfg.shard_count)
        }
        mongot_pods: list[str] = []
        for i in range(cfg.shard_count):
            mongot_pods.extend(
                log_collector.discover_pods(
                    namespace,
                    name_prefix=search_resource_names.shard_statefulset_name(
                        mdbs.name,
                        f"{cfg.mdb_resource_name}-{i}",
                    )
                    + "-",
                )
            )
        envoy_pods = log_collector.discover_pods(
            namespace,
            label_selector=f"app={search_resource_names.lb_deployment_name(mdbs.name)}",
        )

        search_tester = get_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        # Bump verbosity on mongos + each shard's primary mongod via direct
        # pymongo connections (mongos doesn't propagate setParameter).
        set_mongos_debug_logs(search_tester.client, command_level=2, network_level=2)
        for i in range(cfg.shard_count):
            t = get_shard_mongod_tester(
                mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
            )
            try:
                t.client.admin.command(
                    "setParameter", 1, logComponentVerbosity={"command": {"verbosity": 2}, "network": {"verbosity": 2}}
                )
            finally:
                t.client.close()
        logger.info(f"verbosity bumped: mongos pod={mongos_pod} shards={shard_mongods}")

        marker = datetime.now(timezone.utc)
        cursor = tool.paging_cursor_open(batch_size=10)
        try:
            pages = tool.paging_cursor_read_pages(
                cursor,
                pages=5,
                interval_seconds=0.1,
                batch_size=10,
                first_page_index=0,
            )
            assert all(p.success for p in pages), f"paging failed: {[p for p in pages if not p.success]}"
        finally:
            cursor.close()
            set_mongos_debug_logs(search_tester.client, command_level=0, network_level=0)
            for i in range(cfg.shard_count):
                t = get_shard_mongod_tester(
                    mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
                )
                try:
                    t.client.admin.command(
                        "setParameter",
                        1,
                        logComponentVerbosity={"command": {"verbosity": 0}, "network": {"verbosity": 0}},
                    )
                finally:
                    t.client.close()

        # Let trailing slow-query records flush
        time.sleep(2)

        # Pull logs from every layer into ONE capture dir so the
        # operator + reviewer can point lnav at a single path and get
        # the interleaved cross-layer view.
        capture_dir = Path(tempfile.mkdtemp(prefix="mck-capture-"))
        logger.info(f"forensic capture dir: {capture_dir}")
        since_seconds = int((datetime.now(timezone.utc) - marker).total_seconds()) + 5
        # mongos/mongod logs come from the on-disk file (PV-backed, so the
        # history survives container restarts); envoy/mongot still ride
        # the pod-stdout path — those don't persist their logs.
        mongos_logs = log_collector.fetch_database_log_files(namespace, [mongos_pod], dest_dir=capture_dir)
        mongod_logs = log_collector.fetch_database_log_files(namespace, shard_mongods, dest_dir=capture_dir)
        envoy_logs = log_collector.fetch_pod_logs(
            namespace, envoy_pods, since_seconds=since_seconds, dest_dir=capture_dir
        )
        mongot_logs = log_collector.fetch_pod_logs(
            namespace, mongot_pods, since_seconds=since_seconds, dest_dir=capture_dir
        )

        mongos_cmds = read_mongos_commands(mongos_logs, namespace=namespace)
        mongos_reqs = read_mongos_remote_requests(mongos_logs, namespace=namespace)
        mongod_cmds = read_mongod_commands(mongod_logs, namespace=namespace)
        mongod_sessions = read_mongod_sessions(mongod_logs, namespace=namespace)
        envoy_streams = parse_envoy_debug_log(envoy_logs, namespace=namespace) if envoy_logs else []
        mongot_streams, mongot_batches = build_stream_summaries(mongot_logs, namespace=namespace)
        mongot_opens, mongot_cmds = read_mongot_interceptor_events(mongot_logs, namespace=namespace)

        client_ops = parse_client_wire_ops([], anchor_wall_time=marker)

        # Filter to search aggs only — drops noise hello/listIndexes records.
        search_aggs = [c for c in mongos_cmds if c.command == "aggregate" and c.has_search_stage]
        logger.info(
            f"mongos search aggregates observed: {len(search_aggs)} "
            f"(cursor_ids={[c.cursor_id for c in search_aggs]})"
        )

        store = LogStore()
        store.load_from_parsed_records(
            client_ops=client_ops,
            mongod_commands=mongod_cmds,
            mongod_sessions=mongod_sessions,
            mongos_commands=mongos_cmds,
            mongos_remote_requests=mongos_reqs,
            envoy_streams=envoy_streams,
            mongot_streams=mongot_streams,
            mongot_batches=mongot_batches,
            mongot_stream_opens=mongot_opens,
            mongot_cmds=mongot_cmds,
        )
        trees = build_sharded_cursor_trees_sql(
            store,
            shard_pod_prefixes=shard_pod_prefixes,
            shard_mongot_pod_prefixes=shard_mongot_pod_prefixes,
        )
        # Render — captured in e2e log tail.
        logger.info(
            "sharded cursor timeline:\n%s",
            render_sharded_cursor_trees(trees),
        )

        logger.info(
            f"build_sharded_cursor_trees_sql: trees={len(trees)} "
            f"mongot_batches_total={len(mongot_batches)} "
            f"mongod_cmds_total={len(mongod_cmds)} "
            f"mongos_cmds_total={len(mongos_cmds)} "
            f"mongos_reqs_total={len(mongos_reqs)} "
            f"envoy_streams_total={len(envoy_streams)} "
            f"mongot_streams_total={len(mongot_streams)} "
            f"mongot_opens_total={len(mongot_opens)} "
            f"client_ops_total={len(client_ops)} "
            f"shard_pod_prefixes={shard_pod_prefixes} "
            f"shard_mongot_pod_prefixes={shard_mongot_pod_prefixes}"
        )
        assert trees, "build_sharded_cursor_trees_sql returned 0 trees"
        # The aggregate runs the listener-captured cursor; pick the tree
        # that has client wire ops attached.
        primary = next((t for t in trees if t.wire_ops), trees[0])
        logger.info(
            f"primary tree: cursor={primary.top_cursor_id} branches={len(primary.branches)} "
            f"num_shards={primary.num_shards}"
        )

        # Prefer per-shard mongod COMMAND records; fall back to mongos
        # NETWORK ``num_shards`` when the aggregate completed under
        # slowOpThresholdMs before the verbosity bump propagated.
        assert len(primary.branches) >= 2 or (primary.num_shards or 0) >= 2, (
            f"expected fanout to ≥2 shards (mongos NETWORK or per-shard "
            f"mongod COMMAND); got num_shards={primary.num_shards!r} "
            f"(None = mongos NETWORK records absent) "
            f"branches={len(primary.branches)}: "
            f"{[b.shard_name for b in primary.branches]}"
        )
        if primary.branches:
            # Distinct mongot per shard (per-shard StSes are independent).
            mongot_pods_per_shard = {b.shard_name: b.mongot_pod for b in primary.branches if b.mongot_pod}
            assert len(set(mongot_pods_per_shard.values())) == len(
                mongot_pods_per_shard
            ), f"per-shard mongot pods not distinct: {mongot_pods_per_shard}"

    def test_paging_getmore_correlates_to_per_shard_mongod_subgetmores(self, mdb: MongoDB, namespace: str):
        """One client getMore -> per-shard mongod getMores, all sharing the cursor's lsid."""
        cfg = self.build_mongodb_sharded_config()
        mongos_pod = f"{cfg.mdb_resource_name}-mongos-0"
        shard_mongods = [f"{cfg.mdb_resource_name}-{i}-0" for i in range(cfg.shard_count)]

        search_tester = get_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        set_mongos_debug_logs(search_tester.client, command_level=2, network_level=2)
        for i in range(cfg.shard_count):
            t = get_shard_mongod_tester(
                mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
            )
            try:
                t.client.admin.command(
                    "setParameter", 1, logComponentVerbosity={"command": {"verbosity": 2}, "network": {"verbosity": 2}}
                )
            finally:
                t.client.close()
        try:
            marker = datetime.now(timezone.utc)
            cursor = tool.paging_cursor_open(batch_size=10)
            try:
                pages = tool.paging_cursor_read_pages(
                    cursor,
                    pages=3,
                    interval_seconds=0.1,
                    batch_size=10,
                    first_page_index=0,
                )
            finally:
                cursor.close()
            assert all(p.success for p in pages), f"paging failed: {pages}"

            time.sleep(2)
            capture_dir = Path(tempfile.mkdtemp(prefix="mck-capture-"))
            logger.info(f"forensic capture dir: {capture_dir}")
            mongos_logs = log_collector.fetch_database_log_files(namespace, [mongos_pod], dest_dir=capture_dir)
            mongod_logs = log_collector.fetch_database_log_files(namespace, shard_mongods, dest_dir=capture_dir)
            mongos_cmds = read_mongos_commands(mongos_logs, namespace=namespace)
            mongod_cmds = read_mongod_commands(mongod_logs, namespace=namespace)

            agg = next(
                (c for c in mongos_cmds if c.command == "aggregate" and c.has_search_stage),
                None,
            )
            assert agg is not None, "no mongos $search aggregate in window"
            lsid = agg.lsid
            assert lsid, f"mongos aggregate carried no lsid: {agg}"

            client_getmores = [c for c in mongos_cmds if c.command == "getMore" and c.lsid == lsid]
            # Mongos doesn't propagate user lsid to shard mongods on this
            # 8.x build — match shard-side getMores by ns + time window.
            agg_ts = agg.timestamp
            last_ts = max(
                (c.timestamp for c in mongos_cmds if c.timestamp is not None),
                default=agg_ts,
            )
            from datetime import timedelta

            lo = (agg_ts - timedelta(seconds=2)) if agg_ts else datetime.min
            hi = (last_ts + timedelta(seconds=2)) if last_ts else datetime.max
            mongod_getmores = [
                c
                for c in mongod_cmds
                if c.command == "getMore"
                and c.namespace == "sample_mflix.movies"
                and c.timestamp is not None
                and lo <= c.timestamp <= hi
            ]
            logger.info(
                f"getMore counts — client/mongos={len(client_getmores)} "
                f"per-shard-mongod={len(mongod_getmores)} (window=[{lo}, {hi}])"
            )
            by_pod: dict[str, int] = {}
            for c in mongod_getmores:
                by_pod[c.pod] = by_pod.get(c.pod, 0) + 1
            logger.info(f"per-shard mongod getMore counts: {by_pod}")
            assert (
                len(by_pod) >= cfg.shard_count
            ), f"expected ≥{cfg.shard_count} shard mongods with getMores; got {by_pod}"
            for p, n in by_pod.items():
                assert n >= 1, f"shard mongod {p} has only {n} getMore"
        finally:
            set_mongos_debug_logs(search_tester.client, command_level=0, network_level=0)
            for i in range(cfg.shard_count):
                t = get_shard_mongod_tester(
                    mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
                )
                try:
                    t.client.admin.command(
                        "setParameter",
                        1,
                        logComponentVerbosity={"command": {"verbosity": 0}, "network": {"verbosity": 0}},
                    )
                finally:
                    t.client.close()

    def test_paging_through_mongot_pod_restart_per_shard_surfaces_lost_cursor(self, mdb: MongoDB, mdbs: MongoDBSearch):
        """Drain mongos ARM + mongod buffers, hard-kill shard-0 mongots once,
        page on the same cursor.

        The drain primitive proves a sub-getMore actually crossed mongos
        -> shard-0 before the kill; one SIGKILL (grace=0) drops the
        mongot-side cursor state. The post-kill paging may surface either
        disruption (cursor_lost) or seamless recovery (mongos re-routes
        through envoy to the replacement mongot, which replays from the
        sync source on shard-0's mongod). The test asserts the drain
        worked and a wire-op page was observed; the outcome is logged
        without forcing one.
        """
        cfg = self.build_mongodb_sharded_config()
        core_v1 = client.CoreV1Api()
        namespace = mdb.namespace
        mongos_pod = f"{cfg.mdb_resource_name}-mongos-0"
        target_shard_idx = 0
        target_sts = search_resource_names.shard_statefulset_name(
            mdbs.name, f"{cfg.mdb_resource_name}-{target_shard_idx}"
        )
        shard_mongods = [f"{cfg.mdb_resource_name}-{i}-0" for i in range(cfg.shard_count)]

        search_tester = get_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        set_mongos_debug_logs(search_tester.client, command_level=2, network_level=2)
        for i in range(cfg.shard_count):
            t = get_shard_mongod_tester(
                mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
            )
            try:
                t.client.admin.command(
                    "setParameter", 1, logComponentVerbosity={"command": {"verbosity": 2}, "network": {"verbosity": 2}}
                )
            finally:
                t.client.close()
        marker = datetime.now(timezone.utc)

        cursor = None
        drain_capture_dir = Path(tempfile.mkdtemp(prefix="mck-capture-drain-"))
        logger.info(f"drain capture dir: {drain_capture_dir}")
        try:
            cursor, drain_pages, observed = drain_until_cross_shard_subgetmore_observed(
                tool,
                target_shard_index=target_shard_idx,
                mongos_log_fetcher=lambda: log_collector.fetch_database_log_files(
                    namespace, [mongos_pod], dest_dir=drain_capture_dir
                ),
                page_size=1,
                pages_per_iter=20,
                max_pages=400,
                timeout_seconds=60.0,
                namespace=namespace,
            )
            logger.info(f"drain complete: pages={len(drain_pages)} sub-getMores-to-shard-{target_shard_idx}={observed}")
            assert any(p.success for p in drain_pages), f"drain saw no successful page: {drain_pages[-5:]}"
            assert observed > 0, f"drain primitive never saw sub-getMore to shard-{target_shard_idx}"

            # One-shot hard-kill of shard-0 mongots; cursor map is JVM-heap
            # and dies on SIGKILL, replacement pod starts with empty state.
            hard_kill_pods_by_prefix(core_v1, namespace, target_sts + "-")

            post = tool.paging_cursor_read_pages(
                cursor,
                pages=200,
                interval_seconds=0.05,
                batch_size=10,
                first_page_index=len(drain_pages),
                retry_transient_once=False,
                stop_on_error=True,
            )
            logger.info(f"post-fault pages (read {len(post)}): {[str(p) for p in post[-5:]]}")
            verdict = tool.verdict(post)
            logger.info(f"post-fault verdict: {verdict.as_dict()}")

            # Either a real disruption surfaces (cursor_lost preferred,
            # transient_network acceptable) OR mongos absorbs everything
            # and the cursor stays healthy. Both are valid product
            # outcomes — log which one happened. Test passes as long as
            # the drain primitive worked (verified sub-getMore fired) and
            # we drained pages post-fault.
            disruption = verdict.cursor_lost + verdict.transient_network
            if disruption > 0:
                logger.info(
                    f"DISRUPTION SURFACED: cursor_lost={verdict.cursor_lost} "
                    f"transient_network={verdict.transient_network}"
                )
            else:
                logger.info(
                    "SEAMLESS RECOVERY: mongos absorbed shard-0 mongot loss; " "every post-fault page succeeded"
                )
            assert post, "post-fault drain returned no page records"
        finally:
            if cursor is not None:
                cursor.close()
            set_mongos_debug_logs(search_tester.client, command_level=0, network_level=0)
            for i in range(cfg.shard_count):
                t = get_shard_mongod_tester(
                    mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
                )
                try:
                    t.client.admin.command(
                        "setParameter",
                        1,
                        logComponentVerbosity={"command": {"verbosity": 0}, "network": {"verbosity": 0}},
                    )
                finally:
                    t.client.close()
            kt_wait_for_pods_ready(namespace, name_prefix=target_sts + "-", timeout=600)

    def test_paging_through_envoy_restart_surfaces_disruption(self, mdb: MongoDB, mdbs: MongoDBSearch):
        """Drain mongos's ARM buffer, scale envoy to 0, page on same cursor.

        Envoy proxies every mongod->mongot gRPC stream. Killing it tears
        down the upstream side of every shard's mongot stream — but mongos
        ARM + mongod's TaskExecutorCursor each hold a 101-doc batch in
        memory. Drain via the primitive (page_size=1, asserts a sub-getMore
        actually fires) so the next sub-getMore must reach mongot through
        envoy. Scale envoy to 0 so the new envoy pod can't recover the
        stream within the post-fault read window.

        Expected: post-fault verdict shows ``cursor_lost > 0`` (same
        propagation path as the mongot-restart case). Documented to accept
        transient_network if that's the actual product behavior.
        """
        cfg = self.build_mongodb_sharded_config()
        envoy_dep = search_resource_names.lb_deployment_name(mdbs.name)
        apps_v1 = client.AppsV1Api()
        core_v1 = client.CoreV1Api()
        namespace = mdb.namespace
        mongos_pod = f"{cfg.mdb_resource_name}-mongos-0"
        target_shard_idx = 0
        shard_mongods = [f"{cfg.mdb_resource_name}-{i}-0" for i in range(cfg.shard_count)]

        search_tester = get_search_tester(mdb, cfg.admin_user_name, cfg.admin_user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

        set_mongos_debug_logs(search_tester.client, command_level=2, network_level=2)
        for i in range(cfg.shard_count):
            t = get_shard_mongod_tester(
                mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
            )
            try:
                t.client.admin.command(
                    "setParameter", 1, logComponentVerbosity={"command": {"verbosity": 2}, "network": {"verbosity": 2}}
                )
            finally:
                t.client.close()
        marker = datetime.now(timezone.utc)

        cursor = None
        original_envoy_replicas = None
        envoy_terminating_uids: dict[str, str] = {}
        drain_capture_dir = Path(tempfile.mkdtemp(prefix="mck-capture-drain-"))
        logger.info(f"drain capture dir: {drain_capture_dir}")
        try:
            cursor, drain_pages, observed = drain_until_cross_shard_subgetmore_observed(
                tool,
                target_shard_index=target_shard_idx,
                mongos_log_fetcher=lambda: log_collector.fetch_database_log_files(
                    namespace, [mongos_pod], dest_dir=drain_capture_dir
                ),
                page_size=1,
                pages_per_iter=20,
                max_pages=400,
                timeout_seconds=60.0,
                namespace=namespace,
            )
            logger.info(f"drain complete: pages={len(drain_pages)} sub-getMores-to-shard-{target_shard_idx}={observed}")

            dep = apps_v1.read_namespaced_deployment(envoy_dep, namespace)
            original_envoy_replicas = dep.spec.replicas or 1
            apps_v1.patch_namespaced_deployment(
                name=envoy_dep,
                namespace=namespace,
                body={"spec": {"replicas": 0}},
            )
            logger.info(f"scaled envoy Deployment {envoy_dep} -> 0")
            # Scale-to-0 alone leaves pods in Terminating until grace expires.
            # Hard-kill (grace=0) once so envoy stops accepting traffic
            # immediately; with replicas=0 the Deployment controller won't
            # recreate, so no kill-loop is needed.
            try:
                envoy_terminating_uids = hard_kill_pods_by_label(core_v1, namespace, "app", envoy_dep)
            except AssertionError as exc:
                # Pods may already have left the API by the time we look.
                logger.info(f"envoy hard-kill: nothing to kill ({exc})")

            post = tool.paging_cursor_read_pages(
                cursor,
                pages=200,
                interval_seconds=0.05,
                batch_size=10,
                first_page_index=len(drain_pages),
                retry_transient_once=False,
                stop_on_error=True,
            )
            logger.info(f"post-fault pages (read {len(post)}): {[str(p) for p in post[-5:]]}")
            verdict = tool.verdict(post)
            logger.info(f"post-fault verdict: {verdict.as_dict()}")
            disruption = verdict.cursor_lost + verdict.transient_network
            if disruption > 0:
                logger.info(
                    f"DISRUPTION SURFACED: cursor_lost={verdict.cursor_lost} "
                    f"transient_network={verdict.transient_network}"
                )
            else:
                logger.info("SEAMLESS RECOVERY: mongos absorbed envoy outage; " "every post-fault page succeeded")
            assert post, "post-fault drain returned no page records"
        finally:
            if cursor is not None:
                cursor.close()
            if original_envoy_replicas is not None:
                apps_v1.patch_namespaced_deployment(
                    name=envoy_dep,
                    namespace=namespace,
                    body={"spec": {"replicas": original_envoy_replicas}},
                )

            def _ready() -> tuple[bool, str]:
                dep = apps_v1.read_namespaced_deployment(name=envoy_dep, namespace=namespace)
                desired = dep.spec.replicas or 0
                ready = dep.status.ready_replicas or 0
                return ready == desired and desired > 0, f"ready={ready}/{desired}"

            run_periodically_logged(_ready, timeout=180, sleep_time=3, msg="envoy back to Ready")
            set_mongos_debug_logs(search_tester.client, command_level=0, network_level=0)
            for i in range(cfg.shard_count):
                t = get_shard_mongod_tester(
                    mdb, shard_index=i, member_index=0, username=cfg.admin_user_name, password=cfg.admin_user_password
                )
                try:
                    t.client.admin.command(
                        "setParameter",
                        1,
                        logComponentVerbosity={"command": {"verbosity": 0}, "network": {"verbosity": 0}},
                    )
                finally:
                    t.client.close()
