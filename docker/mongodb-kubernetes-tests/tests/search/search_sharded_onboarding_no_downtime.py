"""FUTURE contract: onboarding a shard causes ZERO $search errors.

This encodes the behaviour of the operator's *transparent reroute during initial
sync* feature, developed in parallel on a separate operator branch: while a newly
added shard's mongot is provisioning / initial-syncing, $search for that shard's
slice is routed transparently elsewhere (or returns empty for that slice), so every
query succeeds — no SearchNotEnabled, no "no healthy upstream", no INITIAL_SYNC.

It is the inverse of search_sharded_routed_from_another_shard.py, which asserts the
onboarding gap fails *cleanly*; here we assert the gap does not fail at all.

DORMANT — skipped at module level until the operator feature lands. To activate:
  1. Drop the module-level skip below.
  2. Deploy against an operator build that includes transparent reroute.
  3. Register the marker `e2e_search_sharded_onboarding_no_downtime` and add an
     Evergreen task referencing it (markers are not strict, so collection works
     before then, but CI won't run it).
  4. If the feature is gated, key the skip on the gate instead of removing it.

See docs/investigations/2026-06-09-sharded-search-onboarding-availability.md §8.
"""

from __future__ import annotations

import pytest
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from tests import test_logger
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
    assert_no_index_unavailable,
    delete_mongot_pvcs,
)
from tests.common.search import search_resource_names
from tests.common.search.sharded_search_helper import (
    log_shard_distribution,
    redistribute_chunks_to_new_shard,
    sharded_search_tester,
)
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = [
    pytest.mark.e2e_search_sharded_onboarding_no_downtime,
    pytest.mark.skip(
        reason="requires operator transparent-reroute-during-initial-sync (parallel branch); "
        "see module docstring + docs/investigations/2026-06-09-sharded-search-onboarding-availability.md"
    ),
]

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-sh-no-downtime", shard_count=2)
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
    sample_config = SampleDataAndIndexConfig(extra_doc_count=50_000)

    def admin_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestShardOnboardingNoDowntime:
    """Add a shard data-first, then its mongot group — with no $search error at any point."""

    DB = "sample_mflix"
    COLL = "movies"
    PROBE_COUNT = 40

    def _admin_tester(self):
        return sharded_search_tester(MDBS_NAME, NAMESPACE, MDB.admin_user_name, MDB.admin_user_password)

    def _search_tests(self) -> SearchShardedDeploymentTests:
        t = SearchShardedDeploymentTests()
        t.namespace = NAMESPACE
        t.mdb_config = MDB
        t.search_config = SEARCH
        return t

    def _provision_certs_for_shard_count(self, shard_count: int) -> None:
        MDB.shard_count = shard_count
        source = MongoDBShardedDeploymentTests()
        source.namespace = NAMESPACE
        source.mdb_config = MDB
        source.search_config = SEARCH
        source.install_source_tls_certificates()
        search_tests = self._search_tests()
        search_tests.deploy_lb_certificates()
        search_tests.create_search_tls_certificate()

    def test_baseline_healthy(self, namespace: str):
        tool = SearchConnectivityTool(sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password))
        with SearchAvailabilityBackgroundTester(tool, mode="oneshot") as bg:
            bg.wait_for_operations(self.PROBE_COUNT, timeout=180)
        baseline = bg.verdict
        logger.info(f"[baseline] verdict: {baseline.as_dict()}")
        assert baseline.succeeded > 0 and baseline.failed == 0, f"baseline not healthy: {baseline.as_dict()}"

    def test_onboard_shard_with_zero_errors(self, namespace: str):
        target = MDB.shard_count + 1
        new_shard_name = f"{MDB.mdb_resource_name}-{target - 1}"
        logger.info(f"onboarding shard {new_shard_name} ({MDB.shard_count}->{target}) with zero-error contract")

        self._provision_certs_for_shard_count(target)
        sts_name = search_resource_names.shard_statefulset_name(MDB.mdb_resource_name, new_shard_name, 0)
        delete_mongot_pvcs(namespace, sts_name)

        tool = SearchConnectivityTool(sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password))
        admin = self._admin_tester()

        # The entire onboarding — scale, data migration, mongot-group provisioning, and
        # initial sync — happens inside one availability window. The new feature must keep
        # every probe successful throughout (empty/reduced results allowed, errors not).
        with SearchAvailabilityBackgroundTester(tool, mode="oneshot") as bg:
            mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace).load()
            mdb["spec"]["shardCount"] = target
            mdb.update()
            mdb.assert_reaches_phase(Phase.Running, timeout=900)

            # Data first.
            log_shard_distribution(admin, self.DB, self.COLL, label="[after addShard]")
            final = redistribute_chunks_to_new_shard(admin, self.DB, self.COLL, new_shard_name, min_docs=50, timeout=300)

            # Wire mongotHost BEFORE creating the mongot group — the wiring-induced
            # mongod roll must never interrupt a serving mongot's change stream.
            search_tests = self._search_tests()
            search_tests.test_wire_mongot_host()
            search_tests.test_create_search_resource()

            # Hold the window open across provisioning + initial sync.
            def synced():
                probe = tool.oneshot_search(cache_buster=True, timeout_ms=20_000)
                return probe.success, (probe.error_class or "ok")

            run_periodically(synced, timeout=600, sleep_time=10, msg="new shard mongot to finish initial sync")
            # Sized for the worst case of every probe stalling to its 15s maxTimeMS.
            bg.wait_for_operations(self.PROBE_COUNT, since=bg.operations_count, timeout=self.PROBE_COUNT * 16 + 60)

        verdict = bg.verdict
        logger.info(f"[onboarding, zero-error contract] verdict: {verdict.as_dict()}")
        assert final.get(new_shard_name, 0) >= 50, f"new shard has no data: {final}"
        assert_no_index_unavailable(verdict, "zero-error onboarding")
        assert verdict.failed == 0, (
            f"transparent reroute should yield zero $search errors during onboarding, "
            f"but {verdict.failed} probe(s) failed: {verdict.as_dict()}"
        )
        assert verdict.succeeded > 0, f"no probes ran: {verdict.as_dict()}"
