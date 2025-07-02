from operator import truediv
from typing import List

import kubernetes
import pytest
from kubetester import (
    create_or_update_configmap,
    random_k8s_name,
    read_configmap,
    try_load,
    wait_until,
)
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    return random_k8s_name(f"{namespace}-project-")


@pytest.fixture(scope="module")
def new_project_configmap(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{project_name_prefix}-new-project"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    # ensure certs are created for the members during scale up
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [3, 1, 2])
    resource["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@pytest.fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@pytest.fixture(scope="function")
def mongodb_multi(mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str) -> MongoDBMulti:
    if try_load(mongodb_multi_unmarshalled):
        return mongodb_multi_unmarshalled

    # remove the last element, we are only starting with 2 clusters we will scale up the 3rd one later.
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    # remove one member from the first cluster to start with 2 members
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"][0]["members"] = 2
    return mongodb_multi_unmarshalled


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # read all statefulsets except the last one
    mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients[:-1])


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    mongodb_multi["spec"]["clusterSpecList"].append(
        {"members": 2, "clusterName": member_cluster_clients[2].cluster_name}
    )
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=120)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients, timeout=60)


@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


@skip_if_local
@pytest.mark.e2e_multi_cluster_scale_up_cluster
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])


# From here on, the tests are for verifying that we can change the project of the MongoDBMulti resource even with
# non-sequential member ids in the replicaset.


@pytest.mark.e2e_multi_cluster_scale_up_cluster
class TestNonSequentialMemberIdsInReplicaSet(KubernetesTester):

    def test_scale_up_first_cluster(
        self, mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
    ):
        # Scale up the first cluster to 3 members. This will lead to non-sequential member ids in the replicaset.
        # multi-replica-set-0-0 : 0
        # multi-replica-set-0-1 : 1
        # multi-replica-set-0-2 : 5
        # multi-replica-set-1-0 : 2
        # multi-replica-set-2-0 : 3
        # multi-replica-set-2-1 : 4

        mongodb_multi["spec"]["clusterSpecList"][0]["members"] = 3
        mongodb_multi.update()

        mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients)
        mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)

    def test_change_project(self, mongodb_multi: MongoDBMulti, new_project_configmap: str):
        oldRsMembers = mongodb_multi.get_automation_config_tester().get_replica_set_members(mongodb_multi.name)

        mongodb_multi["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        mongodb_multi.update()

        mongodb_multi.assert_abandons_phase(phase=Phase.Running, timeout=300)
        mongodb_multi.assert_reaches_phase(phase=Phase.Running, timeout=600)

        newRsMembers = mongodb_multi.get_automation_config_tester().get_replica_set_members(mongodb_multi.name)

        # Assert that the replica set member ids have not changed after changing the project.
        assert oldRsMembers == newRsMembers
