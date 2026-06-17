"""E2E: ``$search`` availability while onboarding shards into a single-cluster
sharded MongoDBSearch deployment.

Verifies the routed_from_another_shard contract: while a newly added shard's
mongot is pending (never yet routing-ready), Envoy routes its filter chain to the
cluster-level mongot with the fallback header so ``$search`` through mongos keeps
succeeding; once the mongot is ready the shard latches routing-ready one-way and
later un-readiness fails with the clean no-mongot gap, never an index-state
rejection.
"""

from __future__ import annotations

import time

import kubernetes
import pytest
import yaml
from kubernetes import client
from kubetester import list_matching_pods, wait_for_pods_ready
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBShardedDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchShardedDeploymentTests,
    SearchShardedSampleDataAndIndex,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    assert_clean_no_mongot_gap,
    assert_no_index_unavailable,
    delete_mongot_pvcs,
    mongot_readiness_is_forced_false,
    patch_mongot_readiness_probe_to_false,
    restore_mongot_readiness_probe,
    set_resource_disabled_annotation,
)
from tests.common.search.mc_search_helper import patch_per_cluster_sharded_mongot_host_via_om
from tests.common.search.sharded_search_helper import (
    log_shard_distribution,
    redistribute_chunks_to_new_shard,
    routing_ready_groups,
    shard_route_from_lds,
    sharded_search_tester,
)
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_sharded_routed_from_another_shard

NAMESPACE = get_namespace()
# Mutated as the suite progresses: shard_count grows in stage 1.
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-sh-routed", shard_count=2)
# 1 mongot replica per shard so onboarding a 4th shard fits a single kind node's CPU.
SEARCH = SearchDeploymentConfig(mongot_replicas=1, mongot_cpu_request="200m")
MDBS_NAME = MDB.mdb_resource_name


def _pod_running_not_ready(pod) -> bool:
    ready = any(c.type == "Ready" and c.status == "True" for c in (pod.status.conditions or []))
    return pod.status.phase == "Running" and not ready


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBShardedDeployment(MongoDBShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchShardedDeployment(SearchShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchShardedSampleDataAndIndex):
    mdb_config = MDB
    # ~71k corpus (≈21k restored + 50k synthetic). A smaller corpus means bigger chunks,
    # so rebalancing onto the new shard lands data in a burst (faster than mongot syncs)
    # — a discrete INITIAL_SYNC window. A large/slow migration instead trickles in and the
    # pinned mongot absorbs it incrementally in steady-state (no window to exercise).
    sample_config = SampleDataAndIndexConfig(extra_doc_count=50_000)

    def admin_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestSearchRoutedFromAnotherShard:
    """SearchConnectivityTool against the sharded source, through mongos."""

    PROBE_COUNT = 25

    def assert_data_balanced_across_shards(self, namespace: str):
        tester = sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)
        coll = tester.client["sample_mflix"]["movies"]
        stats = list(coll.aggregate([{"$collStats": {"storageStats": {}}}]))
        per_shard = {s.get("shard") or s.get("host"): s.get("storageStats", {}).get("count", 0) for s in stats}
        total = sum(per_shard.values())
        synthetic = coll.count_documents({"synthetic": True})
        logger.info(f"per-shard movies counts: {per_shard} total={total} synthetic={synthetic}")
        assert (
            len(per_shard) >= MDB.shard_count
        ), f"expected $collStats from {MDB.shard_count} shards; got {len(per_shard)}: {per_shard}"
        for shard, count in per_shard.items():
            assert count >= 50, f"shard {shard} has only {count} docs"
        min_expected = int(total / MDB.shard_count * 0.6)
        for shard, count in per_shard.items():
            assert (
                count >= min_expected
            ), f"shard {shard} has {count} docs; expected ≥ {min_expected} for balanced split"

    def test_data_balanced_across_shards(self, namespace: str):
        self.assert_data_balanced_across_shards(namespace)

    def _provision_certs_for_shard_count(self, namespace: str, shard_count: int) -> None:
        """Create the source-mongod, per-shard-mongot, and LB TLS certs sized for
        ``shard_count`` shards. The operator cannot bring a newly added shard to
        Running without its source cert (``mdb-sh-<mdb>-<idx>-cert``); mongot/Envoy
        for the new shard likewise need their per-shard + LB certs. All cert helpers
        are idempotent (create-or-update), so re-running for existing shards is safe.
        """
        MDB.shard_count = shard_count

        source_tests = MongoDBShardedDeploymentTests()
        source_tests.namespace = namespace
        source_tests.mdb_config = MDB
        source_tests.search_config = SEARCH
        source_tests.install_source_tls_certificates()

        search_tests = self._search_tests()
        search_tests.namespace = namespace
        search_tests.deploy_lb_certificates()
        search_tests.create_search_tls_certificate()

    def _search_tests(self) -> SearchShardedDeploymentTests:
        search_tests = SearchShardedDeploymentTests()
        search_tests.namespace = NAMESPACE
        search_tests.mdb_config = MDB
        search_tests.search_config = SEARCH
        return search_tests

    def test_add_shard(self, namespace: str):
        """Natural-flow shard onboarding with ZERO ``$search`` downtime (variant 1).

        Ordering that makes it hitless: certs → scale the source → wire mongotHost →
        add the shard to the MongoDBSearch → wait for its mongot to latch
        routing-ready (it initial-syncs an empty shard, so this is fast) → only then
        rebalance data onto it. ``$search`` never fans out to a shard that owns no
        chunk, so every step before the rebalance is invisible to queries, and by
        the time data lands the shard serves through its own mongot. A background
        prober runs across the WHOLE flow and must observe zero failures.

        The data-races-ahead ordering (rebalance while the mongot is still pending,
        fallback absorbing live traffic) is covered by
        e2e_search_sharded_onboarding_no_outage; the broken orderings (data landing
        with no search wiring) are characterised by stages 1-2 below."""
        target_shard_count = MDB.shard_count + 1
        new_shard_name = f"{MDB.mdb_resource_name}-{target_shard_count - 1}"
        logger.info(
            f"[onboarding] adding shard: {MDB.shard_count} -> {target_shard_count} (new shard: {new_shard_name})"
        )

        # The new shard's certs must exist before the operator can reconcile it to Running.
        self._provision_certs_for_shard_count(namespace, target_shard_count)

        admin_tester = sharded_search_tester(
            MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password
        )
        tool = SearchConnectivityTool(
            sharded_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)
        )
        # Separate connection for a concurrent paging prober (own cursor/conn).
        paging_tool = SearchConnectivityTool(
            sharded_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)
        )

        # Drop any stale PVC from a prior run so the new mongot syncs fresh and can't
        # latch Ready on a stale index catalog.
        sts_name = search_resource_names.shard_statefulset_name(MDB.mdb_resource_name, new_shard_name, 0)
        delete_mongot_pvcs(namespace, sts_name)

        # 30s probe bound: chunk migration on an oversubscribed kind node can push a
        # healthy $search past 15s; the bound exists to catch wedges, not slowness.
        # Probe back-to-back (interval 0s) with a concurrent paging prober that reopens its
        # cursor every page — each reopen is a fresh $search that needs mongot. The migrated-
        # data INITIAL_SYNC window is only ~5s, so maximize sampling density to cover it.
        with (
            SearchAvailabilityBackgroundTester(
                tool, mode="oneshot", interval_seconds=0.0, query_timeout_ms=30_000
            ) as bg,
            SearchAvailabilityBackgroundTester(
                paging_tool,
                mode="paging",
                interval_seconds=0.0,
                paging_batch_size=5,
                paging_reset_every=1,
                query_timeout_ms=30_000,
            ) as bg_page,
        ):
            mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace).load()
            mdb["spec"]["shardCount"] = target_shard_count
            mdb.update()
            mdb.assert_reaches_phase(Phase.Running, timeout=900)
            logger.info(f"[onboarding] {new_shard_name} mongod reached Running (shard registered, still empty)")

            search_tests = self._search_tests()
            search_tests.wire_mongot_host()
            search_tests.create_search_resource()
            logger.info(f"[onboarding] mongotHost wired + MongoDBSearch updated with {new_shard_name}'s mongot")

            def latched():
                latch = routing_ready_groups(namespace, MDBS_NAME)
                return new_shard_name in latch, f"latch={latch}"

            run_periodically(
                latched, timeout=300, sleep_time=5, msg=f"{new_shard_name} to latch routing-ready (empty sync)"
            )
            # The latch above IS the mongot-readiness gate: the operator sets
            # routingReadyMongotGroups only after the new shard's mongot POD passes its
            # readiness probe, at which point Envoy flips the shard's chain to its own
            # mongot and starts serving. The shard still owns no chunks here, so mongos
            # won't fan $search out to it yet — this window is invisible to queries.
            logger.info(f"[onboarding] LATCH acquired: {new_shard_name} routing-ready -> Envoy fallback dropped")
            at_latch = log_shard_distribution(
                admin_tester,
                "sample_mflix",
                "movies",
                label=f"[onboarding][at-latch] {new_shard_name} ready while empty",
            )
            # Proof precondition: the latch (fallback drop) happened while the new shard
            # owned no chunks — exactly the window that used to risk an INITIAL_SYNC gap.
            assert at_latch.get(new_shard_name, 0) == 0, (
                f"latch-while-empty precondition violated: {new_shard_name} already owns "
                f"{at_latch.get(new_shard_name, 0)} docs at latch; distribution={at_latch}"
            )

            # Hold a short settle floor after the latch — Envoy has flipped the shard's
            # chain to its own mongot — before landing data, so the rebalance begins from
            # a steady routing state rather than racing the CDS/LDS reload.
            time.sleep(20)

            logger.info(
                f"[onboarding] rebalance START: moving chunks onto {new_shard_name} (post-latch, fallback gone)"
            )
            # Move ~20k of ~71k docs in a fast burst (a few big chunks) so the new shard's
            # mongot does a discrete INITIAL_SYNC over the migrated data. Larger targets
            # trickle in (balancer-throttled) and get absorbed incrementally, leaving no
            # window to exercise.
            final = redistribute_chunks_to_new_shard(
                admin_tester, "sample_mflix", "movies", new_shard_name, min_docs=20_000, timeout=900
            )
            logger.info(
                f"[onboarding] rebalance COMPLETE: {new_shard_name} now holds {final.get(new_shard_name, 0)} docs; "
                f"distribution={final}"
            )

            # One more full probe batch with data on the new shard before closing the window.
            bg.wait_for_operations(self.PROBE_COUNT, since=bg.operations_count, timeout=self.PROBE_COUNT * 31 + 60)
        verdict = bg.verdict
        page_verdict = bg_page.verdict
        logger.info(f"[onboarding] oneshot $search verdict across whole window: {verdict.as_dict()}")
        logger.info(f"[onboarding] paging  $search verdict across whole window: {page_verdict.as_dict()}")
        assert (
            final.get(new_shard_name, 0) >= 50
        ), f"new shard {new_shard_name} did not receive data; distribution={final}"
        assert_no_index_unavailable(verdict, "variant1: natural-flow onboarding (oneshot)")
        assert_no_index_unavailable(page_verdict, "variant1: natural-flow onboarding (paging)")
        assert_no_outage(verdict, min_operations=100)
        logger.info(
            f"[onboarding] PROOF: {new_shard_name} latched routing-ready while empty (0 docs), then received "
            f"{final.get(new_shard_name, 0)} docs; $search saw {verdict.succeeded}/{verdict.total_operations} ops "
            f"succeed with {verdict.failed} failures and {verdict.index_unavailable} index_unavailable — "
            f"no INITIAL_SYNC switch, no availability drop."
        )


class TestShardOnboardingAvailabilityStages:
    """Probe ``$search`` availability through mongos while ONE newly added shard is
    onboarded in stages. The danger being characterised: once the balancer places
    collection data on the new shard, mongos fans ``$search`` out to that shard's
    mongod — so if the shard's search path (mongotHost / Envoy route / mongot index)
    is not yet ready, the whole fan-out query can fail even though every other shard
    is healthy. Each stage verifies data + configuration state, then records the
    oneshot query verdict.

    Stages (one shard, walked forward):
      1. shard added + data rebalanced, NO mongotHost, NO per-shard mongot STS -> clean gap
      2. mongotHost set on the shard's mongod, Envoy route + mongot STS still absent -> clean gap
      3. mongot STS + Envoy route deployed, mongot held NOT-ready -> PENDING: the fallback
         serves and queries are already fully clean here (the core proof)
      4. mongot ready -> the shard latches routing-ready and the route flips to its own
         mongot; queries stay clean across the flip
    """

    DB = "sample_mflix"
    COLL = "movies"
    PROBE_COUNT = 25

    # carried across the ordered stages
    onboard_index: int | None = None
    new_shard_name: str | None = None

    # --- helpers ---

    def _admin_tester(self):
        return sharded_search_tester(MDB.mdb_resource_name, NAMESPACE, MDB.admin_user_name, MDB.admin_user_password)

    def _probe_oneshot(self, label: str):
        """Run PROBE_COUNT cache-busted oneshot $search queries through mongos and
        return the verdict (each query is a *new* $search, so it needs every
        data-owning shard's mongot)."""
        tool = SearchConnectivityTool(
            sharded_search_tester(MDB.mdb_resource_name, NAMESPACE, MDB.user_name, MDB.user_password)
        )
        # Bound each probe so a wedged shard is counted as failed, not a hang.
        results = [tool.oneshot_search(cache_buster=True, timeout_ms=15_000) for _ in range(self.PROBE_COUNT)]
        verdict = tool.verdict(results)
        logger.info(f"[{label}] oneshot $search verdict: {verdict.as_dict()}")
        return verdict

    def _shard_mongot_host(self, shard_index: int):
        pod = f"{MDB.mdb_resource_name}-{shard_index}-0"
        conf = yaml.safe_load(
            KubernetesTester.run_command_in_pod_container(pod, NAMESPACE, ["cat", "/data/automation-mongod.conf"])
        )
        return conf.get("setParameter", {}).get("mongotHost")

    def _mongot_sts_name(self, shard_index: int) -> str:
        shard_name = f"{MDB.mdb_resource_name}-{shard_index}"
        return search_resource_names.shard_statefulset_name(MDB.mdb_resource_name, shard_name, 0)

    def _mongot_sts(self, shard_index: int):
        try:
            return client.AppsV1Api().read_namespaced_stateful_set(self._mongot_sts_name(shard_index), NAMESPACE)
        except kubernetes.client.exceptions.ApiException as e:
            if e.status == 404:
                return None
            raise

    def _search_tests(self) -> SearchShardedDeploymentTests:
        t = SearchShardedDeploymentTests()
        t.namespace = NAMESPACE
        t.mdb_config = MDB
        t.search_config = SEARCH
        return t

    def _routing_ready_groups(self) -> list[str]:
        return routing_ready_groups(NAMESPACE, MDBS_NAME)

    def _shard_route(self, shard_name: str) -> tuple[str, dict[str, str]]:
        return shard_route_from_lds(NAMESPACE, MDBS_NAME, shard_name)

    def _assert_pending_fallback(self, shard_name: str, context: str) -> None:
        """Pending contract: not latched in the state CM, and the shard's Envoy chain
        routes to the cluster-level mongot WITH the routed_from_another_shard header."""
        cluster, headers = self._shard_route(shard_name)
        assert (
            cluster == "mongot_cluster_level_cluster"
        ), f"[{context}] shard {shard_name} routes to {cluster!r}, expected cluster-level fallback"
        assert (
            headers.get("search-envoy-metadata-bin") == "CAE="
        ), f"[{context}] shard {shard_name} fallback chain missing routed_from_another_shard header; headers={headers}"
        latch = self._routing_ready_groups()
        assert (
            shard_name not in latch
        ), f"[{context}] shard {shard_name} already latched: routingReadyMongotGroups={latch}"

    def _assert_latched(self, shard_name: str, context: str) -> None:
        """Latched contract: present in the state CM's routingReadyMongotGroups, and
        the shard's Envoy chain routes to its OWN mongot cluster."""
        cluster, _ = self._shard_route(shard_name)
        expected = f"mongot_{shard_name.replace('-', '_')}_cluster"
        assert (
            cluster == expected
        ), f"[{context}] shard {shard_name} routes to {cluster!r}, expected own cluster {expected!r}"
        latch = self._routing_ready_groups()
        assert shard_name in latch, f"[{context}] shard {shard_name} not latched: routingReadyMongotGroups={latch}"

    def _hold_mongot_not_ready(self, namespace: str, onboard_index: int, timeout: int = 180) -> None:
        """Establish a stable not-ready hold on the new shard's mongot for a fault-
        injection stage. The disable-reconciliation annotation must already be set.

        A reconcile already IN FLIGHT when that annotation landed can revert the
        /bin/false probe patch (the annotation only short-circuits reconciles that
        START after it), so re-apply until it sticks; then wait for the rolled pod to
        run not-ready (ready_replicas==0 alone is satisfied by the old pod merely
        terminating)."""
        sts_name = self._mongot_sts_name(onboard_index)

        def held():
            if not mongot_readiness_is_forced_false(namespace, sts_name):
                patch_mongot_readiness_probe_to_false(namespace, sts_name)
                return False, f"{sts_name} probe reverted by in-flight reconcile; re-patched"
            sts = self._mongot_sts(onboard_index)
            ready = (sts.status.ready_replicas or 0) if sts else 0
            if ready != 0:
                return False, f"{sts_name} ready_replicas={ready}"
            pods = list_matching_pods(namespace, name_prefix=sts_name)
            running_not_ready = len(pods) > 0 and all(_pod_running_not_ready(p) for p in pods)
            return running_not_ready, f"pods={[(p.metadata.name, p.status.phase) for p in pods]}"

        run_periodically(held, timeout=timeout, sleep_time=5, msg=f"{sts_name} to be held not-ready")

    # --- stages (definition order = execution order) ---

    def test_01_add_shard_and_rebalance_without_search_config(self, namespace: str):
        tool = SearchConnectivityTool(
            sharded_search_tester(MDB.mdb_resource_name, NAMESPACE, MDB.user_name, MDB.user_password)
        )

        # Baseline: prove $search is healthy on the live topology BEFORE we touch it.
        with SearchAvailabilityBackgroundTester(tool, mode="oneshot") as bg_before:
            bg_before.wait_for_operations(self.PROBE_COUNT, timeout=180)
        baseline = bg_before.verdict
        logger.info(f"[baseline, before onboarding] background verdict: {baseline.as_dict()}")
        assert baseline.succeeded > 0 and baseline.failed == 0, f"baseline not healthy: {baseline.as_dict()}"

        # Onboard the next index beyond the live topology.
        mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace).load()
        current = int(mdb["spec"]["shardCount"])
        onboard_index = current
        new_shard_name = f"{MDB.mdb_resource_name}-{current}"
        TestShardOnboardingAvailabilityStages.onboard_index = onboard_index
        TestShardOnboardingAvailabilityStages.new_shard_name = new_shard_name
        target = current + 1
        logger.info(f"onboarding new shard {new_shard_name} (index {onboard_index}); {current}->{target}")

        # Only the source mongod cert — deliberately no mongot/Envoy cert and no
        # MongoDBSearch update, so the new shard has no search path.
        MDB.shard_count = target
        source_tests = MongoDBShardedDeploymentTests()
        source_tests.namespace = NAMESPACE
        source_tests.mdb_config = MDB
        source_tests.search_config = SEARCH
        source_tests.install_source_tls_certificates()

        # Second window: add the shard and move data onto it WHILE probing, to capture
        # the transition from healthy to broken as data lands on the mongot-less shard.
        with SearchAvailabilityBackgroundTester(tool, mode="oneshot") as bg_during:
            mdb["spec"]["shardCount"] = target
            mdb.update()
            mdb.assert_reaches_phase(Phase.Running, timeout=900)
            admin = self._admin_tester()
            final = redistribute_chunks_to_new_shard(
                admin, self.DB, self.COLL, new_shard_name, min_docs=50, timeout=300
            )
            # Collect a full batch of probes after the data has landed; sized for the
            # worst case of every probe stalling to its 15s maxTimeMS.
            bg_during.wait_for_operations(
                self.PROBE_COUNT, since=bg_during.operations_count, timeout=self.PROBE_COUNT * 16 + 60
            )
        verdict = bg_during.verdict
        logger.info(f"[stage1, onboarding without search config] background verdict: {verdict.as_dict()}")

        # State: data present on the new shard, but no search wiring.
        assert final.get(new_shard_name, 0) >= 50, f"new shard has no data: {final}"
        assert self._shard_mongot_host(onboard_index) is None, "new shard unexpectedly has mongotHost already"
        assert self._mongot_sts(onboard_index) is None, "new shard unexpectedly already has a mongot STS"

        # No mongotHost on the new shard's mongod -> SearchNotEnabled (a clean
        # "search not servable here" signal), never an index-state rejection.
        assert_clean_no_mongot_gap(verdict, "stage1: data on new shard, no search wiring")

    def test_02_configure_mongothost_without_envoy_or_mongot(self, namespace: str):
        onboard_index, new_shard_name = self.onboard_index, self.new_shard_name
        assert onboard_index is not None and new_shard_name is not None, "stage 1 must run first"
        # Point the new shard's mongod at its (not-yet-existing) per-shard Envoy proxy
        # host. mongotHost is set for ALL shards (idempotent for the existing ones).
        mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace).load()
        patch_per_cluster_sharded_mongot_host_via_om(
            mdb=mdb,
            mdbs_resource_name=MDB.mdb_resource_name,
            namespace=namespace,
            shard_count=MDB.shard_count,
            cluster_indexes=[0],
            envoy_proxy_port=SEARCH.envoy_proxy_port,
            multi_cluster=False,
        )
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

        expected_host = search_resource_names.shard_proxy_service_host(
            MDB.mdb_resource_name, new_shard_name, namespace, SEARCH.envoy_proxy_port
        )
        actual_host = self._shard_mongot_host(onboard_index)
        assert actual_host == expected_host, f"mongotHost on new shard = {actual_host!r}, expected {expected_host!r}"
        assert self._mongot_sts(onboard_index) is None, "new shard's mongot STS should still be absent"
        logger.info(f"state OK: {new_shard_name} mongotHost={actual_host}, mongot STS still absent")

        verdict = self._probe_oneshot("stage2: mongotHost set, Envoy route + mongot absent")
        # mongod now routes to the per-shard Envoy host, which has no upstream (no
        # mongot) -> Envoy "no healthy upstream" -> clean gap, never INITIAL_SYNC.
        assert_clean_no_mongot_gap(verdict, "stage2: mongotHost set, Envoy + mongot absent")

    def test_03_fallback_serves_while_pending(self, namespace: str):
        """THE CORE PROOF: while the new shard's mongot has NEVER been routing-ready
        (pending -> fallback active), ``$search`` through mongos SUCCEEDS — the shard
        contributes empty results via the cluster-level mongot. A large initial index
        build therefore causes no query downtime.

        We deploy the new shard's mongot WITHOUT waiting for Running, then immediately
        disable reconciliation and patch its readiness probe to always-fail so the pod
        never reports Ready (never latches routing-ready). The contract surface (Envoy
        fallback chain to the cluster-level mongot with the routed_from_another_shard
        header + the state CM latch) is asserted, then a full probe batch must go
        clean WHILE ready_replicas==0."""
        onboard_index, new_shard_name = self.onboard_index, self.new_shard_name
        assert onboard_index is not None and new_shard_name is not None, "stage 1 must run first"
        search_tests = self._search_tests()
        # Per-shard mongot TLS + LB SAN for the new shard (operator needs these).
        search_tests.create_search_tls_certificate()
        search_tests.deploy_lb_certificates()

        sts_name = self._mongot_sts_name(onboard_index)

        # Clear any stale PVC so this mongot syncs fresh and can't latch Ready on a
        # leftover index catalog.
        delete_mongot_pvcs(namespace, sts_name)

        # Add the new shard to MongoDBSearch WITHOUT waiting for Running — we want to
        # catch the window before the new mongot is ever routing-ready.
        search_tests.create_search_resource(wait=False)

        # As soon as the operator creates the new mongot STS, freeze reconciliation
        # (so the main controller can't latch routing-ready or revert the probe) and
        # force the readiness probe false.
        def sts_exists():
            return self._mongot_sts(onboard_index) is not None, f"{sts_name} not created yet"

        run_periodically(sts_exists, timeout=300, sleep_time=3, msg=f"{sts_name} to be created")
        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
        set_resource_disabled_annotation(mdbs, True)
        self._hold_mongot_not_ready(namespace, onboard_index)
        logger.info(f"state OK: {sts_name} exists but is held not-ready (never routing-ready -> pending)")

        # Contract surface: Envoy routes the new shard's chain to the cluster-level
        # mongot WITH the routed_from_another_shard header, and the shard is absent
        # from the state ConfigMap's routingReadyMongotGroups latch.
        self._assert_pending_fallback(new_shard_name, context="stage3: pending")
        logger.info(f"contract OK: {new_shard_name} pending + Envoy fallback chain with header injection present")

        # CORE ASSERTION: poll until a full probe batch is clean (failed==0, succeeded>0)
        # WHILE the mongot STS still has 0 ready replicas. Allow ~180s for kubelet
        # ConfigMap propagation of the LB config before the batch must go clean.
        def fallback_serves():
            verdict = self._probe_oneshot("stage3: fallback while pending")
            assert_no_index_unavailable(verdict, "stage3: fallback while pending")
            return verdict.failed == 0 and verdict.succeeded > 0, verdict.as_dict()

        run_periodically(
            fallback_serves,
            timeout=180,
            sleep_time=10,
            msg="$search to serve cleanly via fallback while mongot pending",
        )

        # Re-assert the unreadiness/pending invariants right after the clean batch:
        # the success above happened with the new shard's mongot still out of rotation.
        # The probe must still be the forced /bin/false (a revert would invalidate the
        # proof), and the STS must still report 0 ready replicas.
        assert mongot_readiness_is_forced_false(
            namespace, sts_name
        ), f"{sts_name} readiness probe was reverted during the proof — fallback-while-pending not soundly proven"
        sts = self._mongot_sts(onboard_index)
        ready_replicas = (sts.status.ready_replicas or 0) if sts else 0
        assert (
            ready_replicas == 0
        ), f"mongot became ready during the fallback proof: {sts_name} ready_replicas={ready_replicas}"
        self._assert_pending_fallback(new_shard_name, context="stage3: still pending after clean batch")

    def test_04_recovers_when_mongot_ready(self, namespace: str):
        """Clear the disable annotation + restore the probe; the mongot becomes ready,
        the shard latches routing-ready, and queries stay clean across the flip (the
        pending fallback covers the roll back to ready). The latch must hold
        afterwards."""
        onboard_index, new_shard_name = self.onboard_index, self.new_shard_name
        assert onboard_index is not None and new_shard_name is not None, "stage 1 must run first"
        sts_name = self._mongot_sts_name(onboard_index)
        # Resume reconciliation so the operator restores its real readiness probe,
        # then clear the override + roll the pod.
        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
        set_resource_disabled_annotation(mdbs, False)
        restore_mongot_readiness_probe(namespace, sts_name)
        wait_for_pods_ready(namespace, name_prefix=sts_name)

        def queries_recover():
            verdict = self._probe_oneshot("stage4: awaiting recovery")
            assert_no_index_unavailable(verdict, "stage4: awaiting recovery")
            return verdict.failed == 0 and verdict.succeeded > 0, verdict.as_dict()

        run_periodically(queries_recover, timeout=600, sleep_time=15, msg="$search to fully recover on all shards")

        # The shard latches routing-ready once its mongot is ready: its Envoy chain
        # flips to its own mongot cluster.
        # NOTE: probes succeed under both the fallback and the latched Envoy config, so
        # this stage can only assert the latch at the control-plane level (state CM +
        # lds.json CM); the Envoy pod loads the new LDS only after kubelet ConfigMap
        # propagation (up to ~1min). The data-plane flip to the own-mongot route is
        # proven by stage 5: its clean gap can only occur there.
        def latched():
            cluster, _ = self._shard_route(new_shard_name)
            own = f"mongot_{new_shard_name.replace('-', '_')}_cluster"
            return cluster == own and new_shard_name in self._routing_ready_groups(), f"route={cluster}"

        run_periodically(latched, timeout=180, sleep_time=5, msg=f"{new_shard_name} to latch routing-ready")

        final = self._probe_oneshot("stage4: recovered")
        assert final.failed == 0 and final.succeeded > 0, f"queries did not fully recover: {final.as_dict()}"
        self._assert_latched(new_shard_name, context="stage4: latch held after recovery")
        log_shard_distribution(self._admin_tester(), self.DB, self.COLL, label="[final, all shards onboarded]")

    def test_05_post_onboarding_not_ready_fails_clean(self, namespace: str):
        """Once latched, a later mongot un-readiness is the clean own-cluster no-mongot
        gap (NO fallback — the latch is one-way), and the shard NEVER re-enters pending.

        The gap needs propagation before it is observable: the probe patch rolls the
        pod, the replacement must come up Running-but-not-ready, the Endpoints/DNS drop
        must reach Envoy, and Envoy may still be loading the just-latched LDS from
        stage 4 (kubelet ConfigMap propagation lags the CM write by up to ~1min, during
        which probes still succeed via the stale fallback config). So poll until the
        gap appears instead of asserting a single instant batch."""
        onboard_index, new_shard_name = self.onboard_index, self.new_shard_name
        assert onboard_index is not None and new_shard_name is not None, "stage 1 must run first"
        sts_name = self._mongot_sts_name(onboard_index)

        # Pause reconciliation so the operator can't revert the probe override, then
        # hold the (already-latched) mongot not-Ready.
        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
        set_resource_disabled_annotation(mdbs, True)
        self._hold_mongot_not_ready(namespace, onboard_index)
        logger.info(f"state OK: {sts_name} held not-ready post-latch")

        # One-way latch: no fallback re-entry -> Envoy excludes the not-ready backend
        # -> clean "no healthy upstream" gap, never an index-state rejection. The
        # index_unavailable tripwire is the per-batch hard invariant.
        def gap_observed():
            verdict = self._probe_oneshot("stage5: awaiting clean gap")
            assert_no_index_unavailable(verdict, "stage5: latched mongot held not-ready")
            clean = verdict.transient_network + verdict.search_not_enabled + verdict.mongot_unreachable
            return verdict.failed > 0 and clean > 0, verdict.as_dict()

        run_periodically(
            gap_observed,
            timeout=300,
            sleep_time=10,
            msg="latched shard's mongot gap to surface as clean no-mongot failures",
        )
        # CRITICAL: the shard must stay latched (never re-enter pending) — its Envoy
        # chain still targets its own mongot cluster (no fallback re-entry).
        self._assert_latched(new_shard_name, context="stage5: latch held while not-ready")

    def test_06_recovers_again(self, namespace: str):
        """Resume reconciliation + restore the probe; queries fully recover again."""
        onboard_index = self.onboard_index
        assert onboard_index is not None, "stage 1 must run first"
        sts_name = self._mongot_sts_name(onboard_index)
        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
        set_resource_disabled_annotation(mdbs, False)
        restore_mongot_readiness_probe(namespace, sts_name)
        wait_for_pods_ready(namespace, name_prefix=sts_name)

        def queries_recover():
            verdict = self._probe_oneshot("stage6: awaiting recovery")
            assert_no_index_unavailable(verdict, "stage6: awaiting recovery")
            return verdict.failed == 0 and verdict.succeeded > 0, verdict.as_dict()

        run_periodically(queries_recover, timeout=600, sleep_time=15, msg="$search to fully recover again")

        final = self._probe_oneshot("stage6: recovered")
        assert final.failed == 0 and final.succeeded > 0, f"queries did not fully recover: {final.as_dict()}"
        log_shard_distribution(self._admin_tester(), self.DB, self.COLL, label="[final, all shards onboarded]")
