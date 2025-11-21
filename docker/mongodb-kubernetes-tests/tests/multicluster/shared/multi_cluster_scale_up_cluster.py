from typing import List

from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    # read all statefulsets except the last one
    mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients[:-1])


def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]):
    mongodb_multi["spec"]["clusterSpecList"].append(
        {"members": 2, "clusterName": member_cluster_clients[2].cluster_name}
    )
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=120)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)


def test_statefulsets_have_been_scaled_up_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.assert_statefulsets_are_ready(member_cluster_clients, timeout=60)


def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])


# From here on, the tests are for verifying that we can change the project of the MongoDBMulti | MongoDB resource even with
# non-sequential member ids in the replicaset.


class TestNonSequentialMemberIdsInReplicaSet(KubernetesTester):

    def test_scale_up_first_cluster(
        mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]
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

    def test_change_project(mongodb_multi: MongoDBMulti | MongoDB, new_project_configmap: str):
        oldRsMembers = mongodb_multi.get_automation_config_tester().get_replica_set_members(mongodb_multi.name)

        mongodb_multi["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        mongodb_multi.update()

        mongodb_multi.assert_abandons_phase(phase=Phase.Running, timeout=300)
        mongodb_multi.assert_reaches_phase(phase=Phase.Running, timeout=600)

        newRsMembers = mongodb_multi.get_automation_config_tester().get_replica_set_members(mongodb_multi.name)

        # Assert that the replica set member ids have not changed after changing the project.
        assert oldRsMembers == newRsMembers
