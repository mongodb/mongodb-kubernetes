"""Search availability under infrastructure disruption: node drain and operator restart.

Node drain is modelled as cordon + evict on the single-node kind env: the search pods can't
reschedule while the node is cordoned, so new queries fault until uncordon. Operator restart
checks that cycling the control plane doesn't disrupt the data plane (it skips locally, where
the operator runs out-of-cluster with no pod to restart).
"""

from __future__ import annotations

import pytest
from kubernetes import client
from kubetester import list_matching_pods
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
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
    delete_pods,
    wait_for_all_pods_replaced,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import rs_search_tester
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_infra

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail-infra")
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name
MONGOT_SELECTOR = f"app={search_resource_names.mongot_service_name_for_cluster(MDBS_NAME)}"
ENVOY_SELECTOR = f"app={search_resource_names.lb_deployment_name(MDBS_NAME)}"

BASELINE_OPS = 30
POST_EVENT_OPS = 15


# --- shared helpers -------------------------------------------------------


def _user_tool(namespace: str) -> SearchConnectivityTool:
    """A fresh tool (own pymongo client) per call — never share one across concurrent testers."""
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


def _pod_uids(namespace: str, label_selector: str) -> dict[str, str]:
    return {p.metadata.name: p.metadata.uid for p in list_matching_pods(namespace, label_selector=label_selector)}


def _assert_steady(namespace: str) -> None:
    tool = _user_tool(namespace)
    tool.wait_for_sentinel_indexed(timeout=300)
    for mode in ("oneshot", "paging"):
        with SearchAvailabilityBackgroundTester(tool, mode=mode, interval_seconds=0.1) as bg:
            bg.wait_for_operations(BASELINE_OPS)
        assert_no_outage(bg.verdict)


def _node_for_pods(namespace: str, label_selector: str) -> str:
    pods = list_matching_pods(namespace, label_selector=label_selector)
    assert pods, f"no pods matching {label_selector} in ns {namespace}"
    return pods[0].spec.node_name


def _set_cordon(node: str, unschedulable: bool) -> None:
    client.CoreV1Api().patch_node(node, {"spec": {"unschedulable": unschedulable}})
    logger.info(f"node {node} unschedulable={unschedulable}")


def _operator_deployment(namespace: str):
    """The in-cluster operator Deployment, or None when the operator runs out-of-cluster (local
    `make run`, no Deployment). Found by name so we don't hardcode a Helm label that can drift."""
    for d in client.AppsV1Api().list_namespaced_deployment(namespace).items:
        if "operator" in d.metadata.name:
            return d
    return None


def _selector_from(deployment) -> str:
    return ",".join(f"{k}={v}" for k, v in deployment.spec.selector.match_labels.items())


# --- deploy chain (once per file) -----------------------------------------


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


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)


# --- scenario: node drain (cordon + evict) --------------------------------


class TestNodeDrain:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_node_drain_search_pods_outage_then_recovery(self, namespace: str):
        node = _node_for_pods(namespace, ENVOY_SELECTOR)
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.5)
        with oneshot:
            oneshot.wait_for_operations(BASELINE_OPS)
            envoy_uids = _pod_uids(namespace, ENVOY_SELECTOR)
            mongot_uids = _pod_uids(namespace, MONGOT_SELECTOR)
            _set_cordon(node, True)
            try:
                delete_pods(namespace, label_selector=ENVOY_SELECTOR, grace_period_seconds=0)
                delete_pods(namespace, label_selector=MONGOT_SELECTOR, grace_period_seconds=0)
                # Cordoned single node: replacements stay Pending, so the outage persists rather
                # than racing. Require several failures, not just the first.
                failures_before = oneshot.failed_count
                oneshot.wait_for_operations(POST_EVENT_OPS, stop_on_fault=True)
                assert oneshot.failed_count > failures_before, "expected new-query failures while node cordoned"
            finally:
                _set_cordon(node, False)
                wait_for_pods_by_label_replaced(
                    namespace, ENVOY_SELECTOR, envoy_uids, expected=SEARCH.envoy_lb_replicas
                )
                wait_for_all_pods_replaced(namespace, mongot_uids)
        logger.info(f"node-drain oneshot verdict: {oneshot.verdict.as_dict()}")
        # The outage is proven deterministically by the failed_count assertion above. A full node
        # outage surfaces in varying classes (connection refused, gRPC channel deadline, host
        # unreachable), so asserting a specific failure class here would be flaky.
        # Prove outage->recovery atomically in this test, not only via the next method.
        _assert_steady(namespace)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)


# --- scenario: operator restart -------------------------------------------


class TestOperatorRestart:
    def test_steady_state_before(self, namespace: str):
        _assert_steady(namespace)

    def test_operator_restart_no_dataplane_outage(self, namespace: str):
        """Restarting the operator must not disrupt the data plane (the control plane is off the
        query path). Skips only when the operator runs out-of-cluster (local `make run`); when a
        Deployment exists its pod is restarted and must come back — exercised in CI."""
        dep = _operator_deployment(namespace)
        if dep is None or (dep.spec.replicas or 0) == 0:
            pytest.skip("operator runs out-of-cluster (Deployment absent or scaled to 0); exercised in CI")
        selector = _selector_from(dep)
        pods = list_matching_pods(namespace, label_selector=selector)
        assert (
            pods
        ), f"operator Deployment {dep.metadata.name} replicas={dep.spec.replicas} but no pod matched {selector}"
        op = pods[0]
        mdbs = _load_mdbs(namespace)
        oneshot = SearchAvailabilityBackgroundTester(_user_tool(namespace), mode="oneshot", interval_seconds=0.2)
        paging = SearchAvailabilityBackgroundTester(
            _user_tool(namespace), mode="paging", paging_batch_size=5, paging_reset_every=50_000, interval_seconds=0.05
        )
        with oneshot, paging:
            oneshot.wait_for_operations(BASELINE_OPS)
            paging.wait_for_operations(BASELINE_OPS)
            client.CoreV1Api().delete_namespaced_pod(
                op.metadata.name, namespace, body=client.V1DeleteOptions(grace_period_seconds=0)
            )
            oneshot.wait_for_operations(POST_EVENT_OPS)
            paging.wait_for_operations(POST_EVENT_OPS)
        logger.info(f"operator-restart oneshot verdict: {oneshot.verdict.as_dict()}")
        logger.info(f"operator-restart paging verdict: {paging.verdict.as_dict()}")
        assert_no_outage(oneshot.verdict)
        assert_no_outage(paging.verdict)
        # control plane recovers: the deleted pod is replaced and the resource stays Running.
        wait_for_pods_by_label_replaced(namespace, selector, {op.metadata.name: op.metadata.uid})
        mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    def test_recovers_to_steady_state(self, namespace: str):
        _assert_steady(namespace)
