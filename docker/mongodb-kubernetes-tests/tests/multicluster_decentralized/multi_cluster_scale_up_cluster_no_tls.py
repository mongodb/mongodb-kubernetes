"""TLS-free fork of tests/multicluster/multi_cluster_scale_up_cluster.py (M7 Tier 2).

The original is blocked on TLS, which the decentralized POC guards against; stripping the
security block and the with_tls connectivity opts loses nothing this exercise measures — the
point is live coverage of the invariants: adding a brand-new member cluster mid-deployment
(growing from 2 clusters to 3), AC-first ordering, and the non-sequential member-id regression
that surfaces when the project of an already-scaled resource changes. Keep the test bodies
diffable against the original.
"""

from typing import List

import kubernetes
import pytest
from kubetester import create_or_update_configmap, random_k8s_name, read_configmap, try_load, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"


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


@pytest.fixture(scope="function")
def mongodb_multi(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [3, 1, 2])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    # remove the last element, we are only starting with 2 clusters we will scale up the 3rd one later.
    resource["spec"]["clusterSpecList"].pop()
    # remove one member from the first cluster to start with 2 members
    resource["spec"]["clusterSpecList"][0]["members"] = 2
    try_load(resource)
    return resource


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # read all statefulsets except the last one
    mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients[:-1])


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    mongodb_multi["spec"]["clusterSpecList"].append(
        {"members": 2, "clusterName": member_cluster_clients[2].cluster_name}
    )
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=120)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients, timeout=60)


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


@skip_if_local
@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


# From here on, the tests are for verifying that we can change the project of the MongoDBMulti resource even with
# non-sequential member ids in the replicaset.


@pytest.mark.e2e_multi_cluster_decentralized_scale_up_cluster
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
