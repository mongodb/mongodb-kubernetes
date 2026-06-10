"""E2E tests for the search connectivity tool against a single-cluster sharded source."""

from __future__ import annotations

import kubernetes
import pytest
import yaml
from kubernetes import client
from kubetester import wait_for_pods_ready
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester
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
    patch_mongot_readiness_probe_to_false,
    restore_mongot_readiness_probe,
    set_resource_disabled_annotation,
)
from tests.common.search.mc_search_helper import patch_per_cluster_sharded_mongot_host_via_om
from tests.common.search.sharded_search_helper import (
    log_shard_distribution,
    redistribute_chunks_to_new_shard,
    sharded_search_tester,
)
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_sharded_routed_from_another_shard

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-sh-routed", shard_count=2)
# 1 mongot replica per shard so onboarding a 4th shard fits a single kind node's CPU.
SEARCH = SearchDeploymentConfig(mongot_replicas=1)
MDBS_NAME = MDB.mdb_resource_name


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
    # ~70k corpus (≈21k restored + 50k synthetic) so cross-shard fanout paging
    # has enough pages for cursor-loss tests.
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
        """Add a shard *data-first* and characterise the unavoidable onboarding gap.

        Today's mongot+operator cannot preserve $search availability across a shard
        add: there is a window between data landing on the new shard (mongos starts
        fanning $search to it) and that shard's mongot being queryable, and we have
        no per-index readiness to route around it. We assert the gap is the clean
        "no mongot reachable" failure (SearchNotEnabled, then Envoy
        no-healthy-upstream) and *never* a mongot INITIAL_SYNC rejection.

        Data-first ordering is what guarantees that: the new mongot only ever boots
        with its data already present, so it reports Ready only after a full initial
        sync and never latches Ready-while-empty (the case that would later serve a
        still-syncing range and yield INITIAL_SYNC at query time)."""
        target_shard_count = MDB.shard_count + 1
        new_shard_name = f"{MDB.mdb_resource_name}-{target_shard_count - 1}"
        logger.info(f"adding shard: {MDB.shard_count} -> {target_shard_count} (new shard: {new_shard_name})")

        # The new shard's certs must exist before the operator can reconcile it to Running.
        self._provision_certs_for_shard_count(namespace, target_shard_count)

        admin_tester = sharded_search_tester(
            MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password
        )
        tool = SearchConnectivityTool(
            sharded_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)
        )

        # Drop any stale PVC from a prior run so the new mongot syncs fresh and can't
        # latch Ready on a stale index catalog.
        sts_name = search_resource_names.shard_statefulset_name(MDB.mdb_resource_name, new_shard_name, 0)
        delete_mongot_pvcs(namespace, sts_name)

        # Scale the source up; the new shard's mongod comes up with no mongotHost yet.
        mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace).load()
        mdb["spec"]["shardCount"] = target_shard_count
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=900)

        # Migrate data onto the new shard BEFORE its mongot exists. From here mongos
        # fans $search to a data-owning shard with no servable mongot -> clean gap.
        with SearchAvailabilityBackgroundTester(tool, mode="oneshot") as bg_gap:
            log_shard_distribution(admin_tester, "sample_mflix", "movies", label="[immediately after addShard]")
            final = redistribute_chunks_to_new_shard(
                admin_tester, "sample_mflix", "movies", new_shard_name, min_docs=50, timeout=300
            )
            # Sized for the worst case of every probe stalling to its 15s maxTimeMS.
            bg_gap.wait_for_operations(
                self.PROBE_COUNT, since=bg_gap.operations_count, timeout=self.PROBE_COUNT * 16 + 60
            )
        gap = bg_gap.verdict
        logger.info(f"[onboarding gap, data on mongot-less shard] verdict: {gap.as_dict()}")
        assert (
            final.get(new_shard_name, 0) >= 50
        ), f"new shard {new_shard_name} did not receive data; distribution={final}"
        assert_clean_no_mongot_gap(gap, "onboarding gap: data on new shard, no mongot")

        # Wire mongotHost (rolls the shard mongod) BEFORE creating the mongot, so the
        # roll never interrupts a serving mongot's change stream. The mongot then boots
        # with data already present -> genuine initial sync, held out of Envoy by the
        # fresh-server readiness gate until synced.
        search_tests = self._search_tests()
        search_tests.test_wire_mongot_host()
        search_tests.test_create_search_resource()

        # Once the new mongot finishes syncing the present data it joins Envoy rotation
        # and $search recovers — with no index-state rejection ever leaking.
        def recovered():
            results = [tool.oneshot_search(cache_buster=True, timeout_ms=20_000) for _ in range(self.PROBE_COUNT)]
            v = tool.verdict(results)
            assert_no_index_unavailable(v, "post-onboarding recovery")
            return v.failed == 0 and v.succeeded > 0, v.as_dict()

        run_periodically(recovered, timeout=600, sleep_time=15, msg="$search to recover after the new mongot syncs")


class TestShardOnboardingAvailabilityStages:
    """Probe ``$search`` availability through mongos while ONE newly added shard is
    onboarded in stages. The danger being characterised: once the balancer places
    collection data on the new shard, mongos fans ``$search`` out to that shard's
    mongod — so if the shard's search path (mongotHost / Envoy route / mongot index)
    is not yet ready, the whole fan-out query can fail even though every other shard
    is healthy. Each stage verifies data + configuration state, then records the
    oneshot query verdict.

    Stages (one shard, walked forward):
      1. shard added + data rebalanced, NO mongotHost, NO per-shard mongot STS
      2. mongotHost set on the shard's mongod, but Envoy route + mongot STS still absent
      3. mongot STS + Envoy route deployed, but mongot held NOT-ready (out of rotation)
      4. mongot ready -> queries recover
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

    # --- stages (definition order = execution order) ---

    def test_01_add_shard_and_rebalance_without_search_config(self, namespace: str):
        tool = SearchConnectivityTool(
            sharded_search_tester(MDB.mdb_resource_name, NAMESPACE, MDB.user_name, MDB.user_password)
        )

        # test_add_shard already polled $search back to full health (data-first
        # onboarding leaves no lingering catch-up window), so go straight to the
        # baseline: prove $search is healthy BEFORE we touch the topology.
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

    def test_03_deploy_mongot_envoy_but_not_ready(self, namespace: str):
        """Deploy the new shard's mongot, then force it NOT-ready and prove Envoy
        excludes it: queries fail with the clean "no healthy upstream" gap, never an
        index-state rejection. (create_search_resource waits for Running, so the
        mongot has already synced — this stage emulates a not-ready mongot by forcing
        it out of rotation, it does not reproduce a mid-initial-sync pod.)"""
        onboard_index = self.onboard_index
        assert onboard_index is not None, "stage 1 must run first"
        search_tests = self._search_tests()
        # Per-shard mongot TLS + LB SAN for the new shard (operator needs these).
        search_tests.create_search_tls_certificate()
        search_tests.deploy_lb_certificates()

        sts_name = self._mongot_sts_name(onboard_index)

        # Clear any stale PVC so this mongot syncs the already-present data fresh and
        # can't latch Ready on a leftover index catalog.
        delete_mongot_pvcs(namespace, sts_name)

        # Update MongoDBSearch to include the new shard -> operator creates its mongot
        # STS and extends the Envoy config; Running gates on the STS being ready.
        search_tests.test_create_search_resource()
        assert self._mongot_sts(onboard_index) is not None, f"{sts_name} missing after Running"

        # Pause reconciliation so the operator can't revert the probe override
        # (it watches owned STSs — the patch itself would trigger a reconcile).
        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
        set_resource_disabled_annotation(mdbs, True)
        patch_mongot_readiness_probe_to_false(namespace, sts_name)

        # Give the probe time to take effect and confirm the mongot is NOT ready.
        def not_ready():
            sts = self._mongot_sts(onboard_index)
            ready = (sts.status.ready_replicas or 0) if sts else 0
            return ready == 0, f"{sts_name} ready_replicas={ready}"

        run_periodically(not_ready, timeout=120, sleep_time=5, msg=f"{sts_name} to report not-ready")
        logger.info(f"state OK: {sts_name} exists but is held not-ready (forced out of rotation)")

        verdict = self._probe_oneshot("stage3: mongot deployed but not ready")
        # Envoy excludes the not-ready backend -> "no healthy upstream" -> clean gap.
        assert_clean_no_mongot_gap(verdict, "stage3: mongot deployed but held not-ready")

    def test_04_recovers_when_mongot_ready(self, namespace: str):
        onboard_index = self.onboard_index
        assert onboard_index is not None, "stage 1 must run first"
        sts_name = self._mongot_sts_name(onboard_index)
        # Resume reconciliation so the operator restores its real readiness probe,
        # then clear the override + roll the pod.
        mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
        set_resource_disabled_annotation(mdbs, False)
        restore_mongot_readiness_probe(namespace, sts_name)
        wait_for_pods_ready(namespace, name_prefix=sts_name)

        # mongot must finish indexing the migrated range before $search is whole again.
        # Until then probes fail with the clean no-upstream gap; an INITIAL_SYNC
        # rejection here would mean the pod rejoined Envoy while still syncing, so
        # fail hard if one ever surfaces.
        def queries_recover():
            verdict = self._probe_oneshot("stage4: awaiting recovery")
            assert_no_index_unavailable(verdict, "stage4: awaiting recovery")
            return verdict.failed == 0 and verdict.succeeded > 0, verdict.as_dict()

        run_periodically(queries_recover, timeout=600, sleep_time=15, msg="$search to fully recover on all shards")

        final = self._probe_oneshot("stage4: recovered")
        assert final.failed == 0 and final.succeeded > 0, f"queries did not fully recover: {final.as_dict()}"
        log_shard_distribution(self._admin_tester(), self.DB, self.COLL, label="[final, all shards onboarded]")
