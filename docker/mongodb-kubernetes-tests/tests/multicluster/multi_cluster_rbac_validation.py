import time
from typing import List, Optional

import kubernetes
import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import (
    get_operator_pod_restart_count,
    run_periodically,
    wait_for_all_member_clusters_rbac_valid,
    wait_for_member_cluster_condition,
)
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.common.multicluster.multicluster_utils import multi_cluster_service_names
from tests.conftest import generate_and_apply_member_resources
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set-rbac-validation"
RBAC_VERSION_ANNOTATION = "mongodb.com/rbac-version"
BROKEN_RBAC_VERSION = "0.0.0-test"
# The operator re-validates member-cluster RBAC every 60 seconds, so waits that observe a
# validation-status change must allow for at least one re-check interval.
RBAC_RECHECK_TIMEOUT = 180


@pytest.fixture(scope="module")
def mongodb_multi(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
    multi_cluster_operator: Operator,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    # one member per member cluster keeps the deployment small while spanning all members
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1] * len(member_cluster_names))
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.fixture(scope="module")
def broken_cluster_name(member_cluster_names: List[str], central_cluster_name: str) -> str:
    # Break RBAC on a non-central member cluster so the operator's own cluster is unaffected
    # even in setups where the central cluster doubles as a member.
    return next(name for name in member_cluster_names if name != central_cluster_name)


@pytest.fixture(scope="module")
def healthy_cluster_name(member_cluster_names: List[str], broken_cluster_name: str) -> str:
    return next(name for name in member_cluster_names if name != broken_cluster_name)


@pytest.fixture(scope="module")
def operator_restart_count(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> Optional[int]:
    # None means the operator runs locally (no pod in the cluster); the no-restart assertion
    # is skipped in that case.
    return get_operator_pod_restart_count(namespace, MULTI_CLUSTER_OPERATOR_NAME, api_client=central_cluster_client)


def _member_service_account_name(cluster_name: str) -> str:
    # mck-member-<MemberCluster CR metadata.name>-sa, per pkg/resourcenames
    return f"mck-member-{cluster_name}-sa"


def _first_pod_service_name(mongodb_multi: MongoDBMulti, mcc: MultiClusterClient) -> str:
    members = mongodb_multi.get_item_spec(mcc.cluster_name)["members"]
    assert mcc.cluster_index is not None
    return multi_cluster_service_names(mongodb_multi.name, [(mcc.cluster_index, members)])[0]


def _service_is_absent(mcc: MultiClusterClient, name: str, namespace: str) -> bool:
    try:
        mcc.read_namespaced_service(name, namespace)
    except ApiException as e:
        if e.status != 404:
            raise
        return True
    return False


def _wait_for_service_present(mcc: MultiClusterClient, name: str, namespace: str, timeout: int = 120) -> None:
    def check():
        try:
            mcc.read_namespaced_service(name, namespace)
        except ApiException as e:
            if e.status != 404:
                raise
            return False, "service not present yet"
        return True, "service present"

    run_periodically(check, timeout=timeout, sleep_time=5, msg=f"service {name} present on {mcc.cluster_name}")


# TODO(m1kola): revisit when the migration-path E2E lands — the invalid-RBAC scenario may be
# subsumed there (RBAC validation exists primarily for the upgrade scenario).
@pytest.mark.e2e_multi_cluster_rbac_validation
class TestMultiClusterRBACValidation:
    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.wait_for_operator_ready()

    def test_create_mongodb_multi(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)

    def test_member_clusters_start_rbac_valid(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        # requested here so the restart count is snapshotted before RBAC is broken
        operator_restart_count: Optional[int],
    ):
        wait_for_all_member_clusters_rbac_valid(namespace, api_client=central_cluster_client)

    def test_invalid_rbac_annotation_is_reported(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        broken_cluster_name: str,
        member_cluster_clients: List[MultiClusterClient],
    ):
        broken_client = next(mcc for mcc in member_cluster_clients if mcc.cluster_name == broken_cluster_name)
        client.CoreV1Api(api_client=broken_client.api_client).patch_namespaced_service_account(
            _member_service_account_name(broken_cluster_name),
            namespace,
            {"metadata": {"annotations": {RBAC_VERSION_ANNOTATION: BROKEN_RBAC_VERSION}}},
        )

        wait_for_member_cluster_condition(
            namespace,
            broken_cluster_name,
            "False",
            reason="VersionMismatch",
            api_client=central_cluster_client,
            timeout=RBAC_RECHECK_TIMEOUT,
        )

    def test_broken_cluster_is_not_reconciled(
        self,
        namespace: str,
        mongodb_multi: MongoDBMulti,
        broken_cluster_name: str,
        healthy_cluster_name: str,
        member_cluster_clients: List[MultiClusterClient],
    ):
        clients = {mcc.cluster_name: mcc for mcc in member_cluster_clients}
        broken_client = clients[broken_cluster_name]
        healthy_client = clients[healthy_cluster_name]
        broken_service = _first_pod_service_name(mongodb_multi, broken_client)
        healthy_service = _first_pod_service_name(mongodb_multi, healthy_client)

        # Delete an operator-managed per-cluster Service on both clusters, then force a
        # reconcile of the MongoDBMultiCluster CR via an annotation update.
        client.CoreV1Api(api_client=broken_client.api_client).delete_namespaced_service(broken_service, namespace)
        client.CoreV1Api(api_client=healthy_client.api_client).delete_namespaced_service(healthy_service, namespace)
        mongodb_multi.load()
        mongodb_multi["metadata"].setdefault("annotations", {})
        mongodb_multi["metadata"]["annotations"]["mongodb.com/e2e-rbac-validation-trigger"] = str(time.time())
        mongodb_multi.update()

        # The healthy cluster's Service is recreated; the broken cluster is skipped by every
        # workload reconciler, so its Service must stay absent.
        _wait_for_service_present(healthy_client, healthy_service, namespace)
        deadline = time.time() + 60
        while time.time() < deadline:
            assert _service_is_absent(broken_client, broken_service, namespace), (
                f"service {broken_service} was recreated on {broken_cluster_name} "
                "even though the cluster failed RBAC validation"
            )
            time.sleep(5)

    def test_mongodb_multi_stays_running(self, mongodb_multi: MongoDBMulti):
        # Removing the broken cluster from the operator's provider must not disrupt workloads.
        mongodb_multi.assert_reaches_phase(Phase.Running, timeout=120)

    def test_fixing_rbac_restores_reconciliation(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        mongodb_multi: MongoDBMulti,
        broken_cluster_name: str,
        member_cluster_clients: List[MultiClusterClient],
        operator_restart_count: Optional[int],
    ):
        broken_client = next(mcc for mcc in member_cluster_clients if mcc.cluster_name == broken_cluster_name)
        broken_service = _first_pod_service_name(mongodb_multi, broken_client)

        # Re-render and re-apply the member RBAC, restoring the correct rbac-version annotation.
        generate_and_apply_member_resources([broken_cluster_name], namespace)

        wait_for_member_cluster_condition(
            namespace,
            broken_cluster_name,
            "True",
            api_client=central_cluster_client,
            timeout=RBAC_RECHECK_TIMEOUT,
        )
        # Force a reconcile of the MongoDBMultiCluster CR so the re-registered cluster's
        # Service is recreated.
        mongodb_multi.load()
        mongodb_multi["metadata"].setdefault("annotations", {})
        mongodb_multi["metadata"]["annotations"]["mongodb.com/e2e-rbac-validation-trigger"] = str(time.time())
        mongodb_multi.update()
        _wait_for_service_present(broken_client, broken_service, namespace, timeout=RBAC_RECHECK_TIMEOUT)
        mongodb_multi.assert_reaches_phase(Phase.Running, timeout=120)

        # The cluster was removed from and re-added to the operator's provider without an
        # operator restart. The count is None when the operator runs locally (no pod).
        current_restart_count = get_operator_pod_restart_count(
            namespace, MULTI_CLUSTER_OPERATOR_NAME, api_client=central_cluster_client
        )
        if operator_restart_count is not None:
            assert current_restart_count == operator_restart_count, (
                f"operator restarted during the RBAC validation cycle: "
                f"restartCount {operator_restart_count} -> {current_restart_count}"
            )
