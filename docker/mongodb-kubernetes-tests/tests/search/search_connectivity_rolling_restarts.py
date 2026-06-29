"""E2E test: search stays available through rolling restarts of a HA deployment.

Two Envoy replicas + three mongot pods. Background oneshot and paging probes run
throughout native rolling restarts (``kubectl.kubernetes.io/restartedAt``) of the
mongot StatefulSet and the Envoy Deployment, in two scenarios: the tiers rolled
one after the other, and both rolled at the same time (the operator-upgrade
signature). A rolling restart replaces one pod at a time per tier while the rest
keep serving, so we expect ZERO outages either way.
"""

from __future__ import annotations

from datetime import datetime, timezone

import pytest
from kubernetes import client
from kubetester import list_matching_pods
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import (
    PagingAvailabilityFleet,
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
)
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    wait_for_all_pods_replaced,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_rolling_restarts

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-conn-roll")
# HA shape under test: 3 mongot pods behind 2 Envoy replicas.
SEARCH = SearchDeploymentConfig(mongot_replicas=3, envoy_lb_replicas=2)
MDBS_NAME = MDB.mdb_resource_name
MONGOT_STS = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_NAME)
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_DEPLOYMENT = search_resource_names.lb_deployment_name(MDBS_NAME)
ENVOY_SELECTOR = f"app={ENVOY_DEPLOYMENT}"

# Drain enough post-restart pages (per fleet member) that each paging cursor's
# getMore round-trips to mongot (and rolls over to a fresh $search) at least once
# across the disruption.
DRAIN_MIN_PAGES = 100
# Reopen each paging cursor every 20s so a fresh $search — which needs a live
# mongot — is exercised at a steady cadence through the restart window.
PAGING_RESET_AFTER_SECONDS = 20.0
# Concurrent paging cursors. mongod routes each new $search to a random mongot, so
# oversubscribe the replicas (3x) to cover all of them with high probability.
PAGING_FLEET_SIZE = 3 * SEARCH.mongot_replicas


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBDeployment(MongoDBRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchDeployment(SearchRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH

    def build_mdbs(self) -> MongoDBSearch:
        mdbs = super().build_mdbs()
        # SearchRsDeploymentTests wires the managed LB with only externalHostname;
        # pin the Envoy replica count so the proxy tier is itself HA.
        for cluster in mdbs["spec"]["clusters"]:
            cluster.setdefault("loadBalancer", {}).setdefault("managed", {})[
                "replicas"
            ] = self.search_config.envoy_lb_replicas
        return mdbs


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)


class TestSearchConnectivityRollingRestarts:
    def test_rolling_restart_mongot_then_envoy_without_outage(self, namespace: str):
        _assert_zero_outage_through_rolls(namespace, _roll_mongot_then_envoy)

    def test_concurrent_rolling_restart_mongot_and_envoy_without_outage(self, namespace: str):
        # Operator-upgrade signature: a new operator re-renders both the mongot
        # StatefulSet and the Envoy Deployment, so both tiers roll at the same
        # time rather than one after the other.
        _assert_zero_outage_through_rolls(namespace, _roll_mongot_and_envoy_together)


# Module-level helpers


def _assert_zero_outage_through_rolls(namespace: str, roll_fn) -> None:
    mdbs = _load_mdbs(namespace)
    mdbs.assert_reaches_phase(Phase.Running)

    oneshot_tester = SearchAvailabilityBackgroundTester(_user_connectivity_tool(namespace), mode="oneshot")
    paging_fleet = PagingAvailabilityFleet(
        lambda: _user_connectivity_tool(namespace),
        size=PAGING_FLEET_SIZE,
        interval_seconds=0.5,
        paging_reset_after_seconds=PAGING_RESET_AFTER_SECONDS,
    )
    with oneshot_tester, paging_fleet:
        # Warm both probes — the first paging iteration opens each cursor.
        oneshot_tester.wait_for_operations(5)
        paging_fleet.wait_for_operations(5)

        roll_fn(namespace, paging_fleet, oneshot_tester)

    oneshot_verdict = oneshot_tester.verdict
    paging_verdict = paging_fleet.verdict
    logger.info(f"rolling-restart oneshot verdict: {oneshot_verdict.as_dict()}")
    logger.info(f"rolling-restart paging verdict: {paging_verdict.as_dict()}")
    assert_no_outage(oneshot_verdict)
    assert_no_outage(paging_verdict)


def _roll_mongot_then_envoy(namespace: str, paging_fleet: PagingAvailabilityFleet, oneshot_tester) -> None:
    mongot_uids = _pod_uids(namespace, MONGOT_SELECTOR)
    assert (
        len(mongot_uids) == SEARCH.mongot_replicas
    ), f"expected {SEARCH.mongot_replicas} mongot pods before restart; got {sorted(mongot_uids)}"
    _rollout_restart_sts(namespace, MONGOT_STS)
    wait_for_all_pods_replaced(namespace, mongot_uids, timeout=600)
    paging_fleet.wait_for_operations(DRAIN_MIN_PAGES, timeout=300)
    oneshot_tester.wait_for_operations(5)

    envoy_uids = _pod_uids(namespace, ENVOY_SELECTOR)
    assert (
        len(envoy_uids) == SEARCH.envoy_lb_replicas
    ), f"expected {SEARCH.envoy_lb_replicas} Envoy pods before restart; got {sorted(envoy_uids)}"
    _rollout_restart_deployment(namespace, ENVOY_DEPLOYMENT)
    wait_for_pods_by_label_replaced(
        namespace, ENVOY_SELECTOR, envoy_uids, expected=SEARCH.envoy_lb_replicas, timeout=300
    )
    paging_fleet.wait_for_operations(DRAIN_MIN_PAGES, timeout=300)
    oneshot_tester.wait_for_operations(5)


def _roll_mongot_and_envoy_together(namespace: str, paging_fleet: PagingAvailabilityFleet, oneshot_tester) -> None:
    mongot_uids = _pod_uids(namespace, MONGOT_SELECTOR)
    envoy_uids = _pod_uids(namespace, ENVOY_SELECTOR)
    assert (
        len(mongot_uids) == SEARCH.mongot_replicas
    ), f"expected {SEARCH.mongot_replicas} mongot pods before restart; got {sorted(mongot_uids)}"
    assert (
        len(envoy_uids) == SEARCH.envoy_lb_replicas
    ), f"expected {SEARCH.envoy_lb_replicas} Envoy pods before restart; got {sorted(envoy_uids)}"
    # Trigger both rollouts before waiting on either, so the tiers churn at once.
    _rollout_restart_sts(namespace, MONGOT_STS)
    _rollout_restart_deployment(namespace, ENVOY_DEPLOYMENT)
    wait_for_all_pods_replaced(namespace, mongot_uids, timeout=600)
    wait_for_pods_by_label_replaced(
        namespace, ENVOY_SELECTOR, envoy_uids, expected=SEARCH.envoy_lb_replicas, timeout=300
    )
    paging_fleet.wait_for_operations(DRAIN_MIN_PAGES, timeout=300)
    oneshot_tester.wait_for_operations(5)


def _pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


def _rollout_restart_sts(namespace: str, sts_name: str) -> None:
    now = datetime.now(timezone.utc).isoformat()
    client.AppsV1Api().patch_namespaced_stateful_set(
        name=sts_name,
        namespace=namespace,
        body={"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": now}}}}},
    )
    logger.info(f"rollout-restart StatefulSet {sts_name} (restartedAt={now})")


def _rollout_restart_deployment(namespace: str, deployment_name: str) -> None:
    now = datetime.now(timezone.utc).isoformat()
    client.AppsV1Api().patch_namespaced_deployment(
        name=deployment_name,
        namespace=namespace,
        body={"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": now}}}}},
    )
    logger.info(f"rollout-restart Deployment {deployment_name} (restartedAt={now})")


def _user_connectivity_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


def _load_mdbs(namespace: str) -> MongoDBSearch:
    helper = SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB.mdb_resource_name,
        mdbs_resource_name=MDBS_NAME,
        ca_configmap_name=MDB.ca_configmap_name,
    )
    return helper.mdbs_for_ext_rs_source(
        MDB.mongot_user_name,
        members=MDB.rs_members,
        lb_mode="Managed",
        clusters=[{"replicas": SEARCH.mongot_replicas}],
    )
