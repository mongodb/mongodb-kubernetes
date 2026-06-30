"""E2E: ZERO ``$search`` downtime while shard onboarding races data migration
against mongot readiness.

This is variant 2 of the onboarding-availability story (variant 1 — the
wait-for-latch natural flow — is ``test_add_shard`` in
search_sharded_routed_from_another_shard.py): data is rebalanced onto the new
shard IMMEDIATELY, while its mongot group is still pending, so the Envoy
routed_from_another_shard fallback must absorb live query traffic. Every mongot
pod carries a startup-delay init container that pins the pending window open
long enough that the overlap (data on the shard while not latched) is
guaranteed, and the test PROVES the overlap occurred rather than assuming it.

The delay lives in its own suite (rather than the routed_from_another_shard one)
because the per-cluster statefulSet override applies to every mongot pod from
bootstrap on — folding it into that suite would slow all six of its staged
fault-injection tests."""

from __future__ import annotations

from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import mark
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
from tests.common.search.connectivity import SearchConnectivityTool, assert_no_index_unavailable, delete_mongot_pvcs
from tests.common.search.sharded_search_helper import (
    redistribute_chunks_to_new_shard,
    routing_ready_groups,
    shard_route_from_lds,
    sharded_search_tester,
    wait_for_envoy_loaded_shard_chain,
)
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = mark.e2e_search_sharded_onboarding_no_outage

NAMESPACE = get_namespace()
MDB_RESOURCE_NAME = "mdb-sh-noout"
MDBS_NAME = MDB_RESOURCE_NAME
INITIAL_SHARD_COUNT = 2
DB = "sample_mflix"
COLL = "movies"

# Pins the new mongot's pending window open: it must comfortably exceed the time
# from the MongoDBSearch update to the balancer placing min_docs on the new shard
# (envoy chain mount gate + first ~1MB migrations, typically well under 2 min).
MONGOT_STARTUP_DELAY_SECONDS = 180

# Mutated as the suite progresses: shard_count grows in the onboarding phase.
MDB = MongoDBDeploymentConfig(mdb_resource_name=MDB_RESOURCE_NAME, shard_count=INITIAL_SHARD_COUNT)
# create_timeout covers the per-pod startup delay.
SEARCH = SearchDeploymentConfig(mongot_replicas=1, mongot_cpu_request="200m", mongot_memory="1Gi", create_timeout=1800)


class DelayedMongotSearchDeploymentTests(SearchShardedDeploymentTests):
    """SC sharded search deploy whose mongot pods sleep in an init container
    before starting."""

    def search_clusters(self) -> list:
        clusters = super().search_clusters()
        clusters[0]["statefulSet"] = {
            "spec": {
                "template": {
                    "spec": {
                        "initContainers": [
                            {
                                "name": "startup-delay",
                                "image": "busybox",
                                "command": ["sh", "-c", f"sleep {MONGOT_STARTUP_DELAY_SECONDS}"],
                                # Required by PodSecurity "restricted" enforced on the test namespace.
                                "securityContext": {
                                    "allowPrivilegeEscalation": False,
                                    "runAsNonRoot": True,
                                    "capabilities": {"drop": ["ALL"]},
                                    "seccompProfile": {"type": "RuntimeDefault"},
                                },
                            }
                        ]
                    }
                }
            }
        }
        return clusters


def _admin_tester():
    return sharded_search_tester(MDB_RESOURCE_NAME, NAMESPACE, MDB.admin_user_name, MDB.admin_user_password)


def _user_tool() -> SearchConnectivityTool:
    return SearchConnectivityTool(sharded_search_tester(MDB_RESOURCE_NAME, NAMESPACE, MDB.user_name, MDB.user_password))


def _search_tests() -> DelayedMongotSearchDeploymentTests:
    t = DelayedMongotSearchDeploymentTests()
    t.namespace = NAMESPACE
    t.mdb_config = MDB
    t.search_config = SEARCH
    return t


def _all_search_doc_ids(tester) -> set:
    """All ``_id``s the wildcard ``$search`` returns — the full searchable corpus
    through mongos. Used as the completeness baseline around onboarding."""
    cursor = tester.client[DB][COLL].aggregate(
        [
            {"$search": {"index": "default", "wildcard": {"query": "*", "path": "title", "allowAnalyzedField": True}}},
            {"$project": {"_id": 1}},
        ]
    )
    return {doc["_id"] for doc in cursor}


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBShardedDeployment(MongoDBShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchShardedDeployment(DelayedMongotSearchDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchShardedSampleDataAndIndex):
    mdb_config = MDB
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestImmediateRebalanceOnboarding:
    """Onboard one shard with data migration racing ahead of mongot readiness.

    The background prober runs across the ENTIRE flow (scale-up, mongotHost
    wiring, MongoDBSearch update, rebalance-while-pending, latch flip) and must
    observe zero failures: the pending window is served by the Envoy fallback."""

    PROBE_COUNT = 25

    def test_baseline_healthy(self, namespace: str):
        with SearchAvailabilityBackgroundTester(_user_tool(), mode="oneshot") as bg:
            bg.wait_for_operations(self.PROBE_COUNT, timeout=180)
        baseline = bg.verdict
        logger.info(f"[baseline] verdict: {baseline.as_dict()}")
        assert baseline.succeeded > 0 and baseline.failed == 0, f"baseline not healthy: {baseline.as_dict()}"

    def test_onboard_shard_with_immediate_rebalance(self, namespace: str):
        target_shard_count = MDB.shard_count + 1
        new_shard_name = f"{MDB_RESOURCE_NAME}-{target_shard_count - 1}"
        logger.info(f"onboarding {new_shard_name}: {MDB.shard_count} -> {target_shard_count}, rebalance immediately")

        # Completeness baseline: the exact searchable corpus before the topology change.
        user_tester = sharded_search_tester(MDB_RESOURCE_NAME, namespace, MDB.user_name, MDB.user_password)
        baseline_ids = _all_search_doc_ids(user_tester)
        assert len(baseline_ids) > 1000, f"baseline corpus suspiciously small: {len(baseline_ids)} docs"
        logger.info(f"baseline searchable corpus: {len(baseline_ids)} docs")

        # Certs for the new shard (source mongod + per-shard mongot + LB SANs).
        MDB.shard_count = target_shard_count
        source_tests = MongoDBShardedDeploymentTests()
        source_tests.namespace = namespace
        source_tests.mdb_config = MDB
        source_tests.search_config = SEARCH
        source_tests.install_source_tls_certificates()
        search_tests = _search_tests()
        search_tests.deploy_lb_certificates()
        search_tests.create_search_tls_certificate()

        # Drop any stale PVC from a prior run so the new mongot syncs fresh.
        sts_name = search_resource_names.shard_statefulset_name(MDBS_NAME, new_shard_name, 0)
        delete_mongot_pvcs(namespace, sts_name)

        admin_tester = _admin_tester()
        # 30s probe bound: chunk migration on an oversubscribed kind node can push a
        # healthy $search past 15s; the bound exists to catch wedges, not slowness.
        with SearchAvailabilityBackgroundTester(
            _user_tool(), mode="oneshot", interval_seconds=1.0, query_timeout_ms=30_000
        ) as bg:
            mdb = MongoDB(name=MDB_RESOURCE_NAME, namespace=namespace).load()
            mdb["spec"]["shardCount"] = target_shard_count
            mdb.update()
            mdb.assert_reaches_phase(Phase.Running, timeout=900)

            search_tests.wire_mongot_host()
            # Add the shard to the search CR WITHOUT waiting for Running — the point
            # is to move data while the new mongot is still pending.
            search_tests.create_search_resource(wait=False)

            # Gate data movement on the proxies having LOADED the new shard's chain.
            # Production ordering gives this for free (the chain is published minutes
            # before the shard can own a chunk); this suite's external-source flow
            # updates the CR late, so without the gate we would be measuring kubelet
            # ConfigMap propagation instead of the fallback.
            wait_for_envoy_loaded_shard_chain(namespace, MDBS_NAME, new_shard_name)

            final = redistribute_chunks_to_new_shard(admin_tester, DB, COLL, new_shard_name, min_docs=50, timeout=300)
            docs = final.get(new_shard_name, 0)
            assert docs >= 50, f"new shard {new_shard_name} did not receive data; distribution={final}"

            # PROVE the fallback window: data is on the shard while it is NOT latched
            # and its chain still targets the cluster-level fallback with the header.
            latch = routing_ready_groups(namespace, MDBS_NAME)
            assert new_shard_name not in latch, (
                f"{new_shard_name} latched before data landed — no fallback window occurred; "
                f"increase MONGOT_STARTUP_DELAY_SECONDS (latch={latch})"
            )
            cluster, headers = shard_route_from_lds(namespace, MDBS_NAME, new_shard_name)
            assert cluster == "mongot_cluster_level_cluster", f"expected fallback route, got {cluster!r}"
            assert headers.get("search-envoy-metadata-bin") == "CAE=", f"missing fallback header; headers={headers}"
            # CR-level pending-ness, independent of the latch CM: the search resource
            # must not have reached Running with the new mongot still starting up.
            mdbs_phase = (MongoDBSearch(name=MDBS_NAME, namespace=namespace).load()["status"] or {}).get("phase")
            assert mdbs_phase != "Running", "MongoDBSearch already Running during the fallback window"
            logger.info(f"fallback window proven: {docs} docs on {new_shard_name} while pending (phase={mdbs_phase})")

            # A full probe batch entirely inside the pending-with-data window.
            bg.wait_for_operations(self.PROBE_COUNT, since=bg.operations_count, timeout=self.PROBE_COUNT * 31 + 60)

            # Natural recovery: the mongot finishes its (delayed) startup + sync,
            # the shard latches, and the route flips to its own mongot cluster.
            mdbs = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
            mdbs.assert_reaches_phase(Phase.Running, timeout=1800)

            def latched():
                latch = routing_ready_groups(namespace, MDBS_NAME)
                return new_shard_name in latch, f"latch={latch}"

            run_periodically(latched, timeout=300, sleep_time=5, msg=f"{new_shard_name} to latch routing-ready")

            # The balancer finishes redistributing (compliant = per-shard spread below
            # 3 x the 1MB chunkSize), then the searchable corpus through mongos must
            # converge back to the exact baseline id set — migrated ranges reappear
            # once the new shard's mongot has indexed them.
            def balancer_compliant():
                status = admin_tester.client.admin.command("balancerCollectionStatus", f"{DB}.{COLL}")
                return bool(status.get("balancerCompliant")), f"balancerCollectionStatus={status}"

            run_periodically(
                balancer_compliant, timeout=600, sleep_time=10, msg=f"balancer to finish redistributing {DB}.{COLL}"
            )

            def corpus_complete():
                ids = _all_search_doc_ids(user_tester)
                missing = len(baseline_ids - ids)
                extra = len(ids - baseline_ids)
                return (
                    ids == baseline_ids,
                    f"baseline={len(baseline_ids)} now={len(ids)} missing={missing} extra={extra}",
                )

            run_periodically(
                corpus_complete,
                timeout=600,
                sleep_time=15,
                msg="searchable corpus to match the pre-onboarding baseline",
            )
        verdict = bg.verdict
        logger.info(f"[variant2 immediate-rebalance onboarding] verdict: {verdict.as_dict()}")
        assert_no_index_unavailable(verdict, "variant2: immediate-rebalance onboarding")
        assert_no_outage(verdict, min_operations=100)
