from typing import List

import kubernetes
import pytest
from kubetester import create_or_update, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace)
    resource.set_version(custom_mdb_version)

    # TODO: incorporate this into the base class.
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    return create_or_update(resource)


@pytest.mark.e2e_multi_cluster_replica_set_deletion
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_replica_set_deletion
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@pytest.mark.e2e_multi_cluster_replica_set_deletion
def test_automation_config_has_been_updated(mongodb_multi: MongoDBMulti):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    processes = tester.get_replica_set_processes(mongodb_multi.name)
    assert len(processes) == 5


@pytest.mark.e2e_multi_cluster_replica_set_deletion
def test_delete_mongodb_multi(
    mongodb_multi: MongoDBMulti,
):
    mongodb_multi.load()

    # TODO: uncomment when change is merged.
    # mongodb_multi.delete()

    body = kubernetes.client.V1DeleteOptions()
    mongodb_multi.api.delete_namespaced_custom_object(
        mongodb_multi.group,
        mongodb_multi.version,
        mongodb_multi.namespace,
        mongodb_multi.plural,
        mongodb_multi.name,
        body=body,
    )

    def wait_for_deleted() -> bool:
        try:
            mongodb_multi.load()
            return False
        except kubernetes.client.ApiException:
            return True

    wait_until(wait_for_deleted, timeout=60)


@pytest.mark.e2e_multi_cluster_replica_set_deletion
def test_deployment_has_been_removed_from_automation_config():
    def wait_until_automation_config_is_clean() -> bool:
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        try:
            tester.assert_empty()
            return True
        except AssertionError as e:
            print(e)
            return False

    wait_until(wait_until_automation_config_is_clean, timeout=60)


@pytest.mark.e2e_multi_cluster_replica_set_deletion
def test_kubernetes_resources_have_been_cleaned_up(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    def wait_until_secrets_are_removed() -> bool:
        try:
            mongodb_multi.read_services(member_cluster_clients)
            return False
        except kubernetes.client.ApiException as e:
            print(e)
            return True

    def wait_until_statefulsets_are_removed() -> bool:
        try:
            mongodb_multi.read_statefulsets(member_cluster_clients)
            return False
        except kubernetes.client.ApiException as e:
            print(e)
            return True

    def wait_until_configmaps_are_removed() -> bool:
        try:
            mongodb_multi.read_configmaps(member_cluster_clients)
            return False
        except kubernetes.client.ApiException as e:
            print(e)
            return True

    wait_until(wait_until_secrets_are_removed, timeout=60)
    wait_until(wait_until_statefulsets_are_removed, timeout=60)
    wait_until(wait_until_configmaps_are_removed, timeout=60)
