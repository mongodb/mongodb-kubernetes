import os
import time
from typing import Optional

import kubernetes
import kubernetes.client
from kubetester import (
    delete_statefulset,
    get_statefulset,
    read_configmap,
    try_load,
    update_configmap,
)
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import (
    get_env_var_or_fail,
    is_default_architecture_static,
    is_multi_cluster,
    run_periodically,
    skip_if_local,
)
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_central_cluster_client, get_member_cluster_api_client
from tests.multicluster.conftest import cluster_spec_list
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_all_sharded_cluster_pod_names,
)

MEMBER_CLUSTERS = ["kind-e2e-cluster-1", "kind-e2e-cluster-2", "kind-e2e-cluster-3"]
FAILED_MEMBER_CLUSTER_INDEX = 2
FAILED_MEMBER_CLUSTER_NAME = MEMBER_CLUSTERS[FAILED_MEMBER_CLUSTER_INDEX]
RESOURCE_NAME = "sh-disaster-recovery"

logger = test_logger.get_test_logger(__name__)


# We test a simple disaster recovery scenario: we lose one cluster without losing the majority.
# We ensure that the operator correctly ignores the unhealthy cluster in the subsequent reconciliation,
# and we can still scale. The DR procedure requires to first scale down all unhealthy members to be able
# to reconfigure the deployment further.


def is_cloud_qa() -> bool:
    return os.getenv("ops_manager_version", "cloud_qa") == "cloud_qa"


@mark.e2e_multi_cluster_sharded_disaster_recovery
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@fixture(scope="function")
def ops_manager(
    namespace,
    ops_manager_issuer_ca_configmap: str,
    app_db_issuer_ca_configmap: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> Optional[MongoDBOpsManager]:
    if is_cloud_qa():
        return None

    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["security"] = {}
    resource["spec"]["applicationDatabase"]["security"] = {}
    resource["spec"]["backup"] = {"enabled": False}

    if is_multi_cluster():
        resource["spec"]["topology"] = "MultiCluster"
        resource["spec"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-1"], [1])
        resource["spec"]["applicationDatabase"]["topology"] = "MultiCluster"
        resource["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(["kind-e2e-cluster-1"], [3])
        resource.api = kubernetes.client.CustomObjectsApi(api_client=get_central_cluster_client())

    return resource


@mark.skipif(is_cloud_qa(), reason="OM deployment is skipped if the test is executed against Cloud QA")
@mark.e2e_multi_cluster_sharded_disaster_recovery
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str, ops_manager: MongoDBOpsManager) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-scale-shards.yaml"), namespace=namespace, name=RESOURCE_NAME
    )

    if try_load(resource):
        return resource

    # this allows us to reuse this test in both variants: with OMs and with Cloud QA
    # if this is not executed, the resource uses default values for project and credentials (my-project/my-credentials)
    # which are created up by the preparation scripts.
    if not is_cloud_qa():
        resource.configure(ops_manager, RESOURCE_NAME, api_client=get_central_cluster_client())

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    enable_multi_cluster_deployment(
        resource=resource,
        shard_members_array=[2, 1, 2],
        mongos_members_array=[1, 0, 2],
        configsrv_members_array=[2, 1, 2],
    )

    return resource.update()


@fixture(scope="module")
def config_version_store():
    class ConfigVersion:
        version = 0

    return ConfigVersion()


@mark.e2e_multi_cluster_sharded_disaster_recovery
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_disaster_recovery
class TestDeployShardedClusterWithFailedCluster:
    def test_create_sharded_cluster(self, sc: MongoDB, config_version_store):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)
        config_version_store.version = sc.get_automation_config_tester().automation_config["version"]
        logger.debug(f"Automation Config Version after initial deployment: {config_version_store.version}")

    def test_remove_cluster_from_operator_member_list_to_simulate_it_is_unhealthy(
        self, namespace, central_cluster_client: kubernetes.client.ApiClient, multi_cluster_operator: Operator
    ):
        operator_cm_name = "mongodb-enterprise-operator-member-list"
        logger.debug(f"Deleting cluster {FAILED_MEMBER_CLUSTER_NAME} from configmap {operator_cm_name}")
        member_list_cm = read_configmap(
            namespace,
            operator_cm_name,
            api_client=central_cluster_client,
        )
        # this if is only for allowing re-running the test locally, without it the test function could be executed
        # only once until the map is populated again by running prepare-local-e2e run again
        if FAILED_MEMBER_CLUSTER_NAME in member_list_cm:
            member_list_cm.pop(FAILED_MEMBER_CLUSTER_NAME)

        # this will trigger operators restart as it panics on changing the configmap
        update_configmap(
            namespace,
            operator_cm_name,
            member_list_cm,
            api_client=central_cluster_client,
        )

        # sleeping to ensure the operator will suicide after config map is changed
        # TODO: as part of https://jira.mongodb.org/browse/CLOUDP-288588, and when we re-activate this test, ensure
        #  this sleep is really nededed or if the subsquent call to multi_cluster_operator.assert_is_running() is enough
        time.sleep(30)

    @skip_if_local
    # Modifying the configmap triggers an (intentional) panic, the pod should restart.
    # Operator process restart has to be done manually when running locally.
    def test_operator_has_restarted(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_delete_all_statefulsets_in_failed_cluster(
        self, sc: MongoDB, central_cluster_client: kubernetes.client.ApiClient
    ):
        shards_sts_names = [
            sc.shard_statefulset_name(shard_idx, FAILED_MEMBER_CLUSTER_INDEX)
            for shard_idx in range(sc["spec"]["shardCount"])
        ]
        config_server_sts_name = sc.config_srv_statefulset_name(FAILED_MEMBER_CLUSTER_INDEX)
        mongos_sts_name = sc.mongos_statefulset_name(FAILED_MEMBER_CLUSTER_INDEX)

        all_sts_names = shards_sts_names + [config_server_sts_name, mongos_sts_name]
        logger.debug(
            f"Deleting {len(all_sts_names)} statefulsets in failed cluster, statefulsets names: {all_sts_names}"
        )

        for sts_name in shards_sts_names + [config_server_sts_name, mongos_sts_name]:
            try:
                # delete all statefulsets in failed member cluster to simulate full cluster outage
                delete_statefulset(
                    sc.namespace,
                    sts_name,
                    propagation_policy="Background",
                    api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
                )
            except kubernetes.client.ApiException as e:
                if e.status != 404:
                    raise e

        def statefulset_is_deleted(namespace: str, name: str, api_client=Optional[kubernetes.client.ApiClient]):
            try:
                get_statefulset(namespace, name, api_client=api_client)
                return False
            except kubernetes.client.ApiException as e:
                if e.status == 404:
                    return True
                else:
                    raise e

        for sts_name in shards_sts_names + [config_server_sts_name, mongos_sts_name]:
            run_periodically(
                lambda: statefulset_is_deleted(
                    sc.namespace,
                    sts_name,
                    api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
                ),
                timeout=120,
            )

    def test_sharded_cluster_is_stable(self, sc: MongoDB, config_version_store):
        sc.assert_reaches_phase(Phase.Running)
        # Automation Config shouldn't change when we lose a cluster
        expected_version = config_version_store.version
        # in non-static, every restart of the operator increases version of ac due to agent upgrades
        if not is_default_architecture_static():
            expected_version += 1

        assert expected_version == sc.get_automation_config_tester().automation_config["version"]

        logger.debug(f"Automation Config Version after losing cluster: {config_version_store.version}")


@mark.e2e_multi_cluster_sharded_disaster_recovery
class TestScaleShardsAndMongosToZeroFirst:
    def test_scale_shards_and_mongos_to_zero_first(self, sc: MongoDB):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(MEMBER_CLUSTERS, [2, 1, 0])  # cluster3: 2->0
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(MEMBER_CLUSTERS, [1, 0, 0])  # cluster3: 2->0
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(MEMBER_CLUSTERS, [2, 1, 0])  # cluster3: 2->0
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_expected_processes_in_ac(self, sc: MongoDB):
        all_process_names = [p["name"] for p in sc.get_automation_config_tester().get_all_processes()]
        assert set(get_all_sharded_cluster_pod_names(sc)) == set(all_process_names)


@mark.e2e_multi_cluster_sharded_disaster_recovery
class TestMoveFailedToHealthyClusters:
    # simulate that we expand on the healthy clusters to have the same number of nodes as before "disaster"
    def test_move_failed_to_healthy_clusters(self, sc: MongoDB):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(MEMBER_CLUSTERS, [3, 2, 0])  # cluster1: 1->3
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(
            MEMBER_CLUSTERS, [2, 1, 0]
        )  # cluster1: 1->2, cluster2: 0->1
        # we don't get back to 6 members as adding each csrs node causes all mongos to perform rolling restart - it's taking too long
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(
            MEMBER_CLUSTERS, [3, 2, 0]
        )  # cluster1: 2->3, cluster2: 1->2
        sc.update()

        # timeout is large due to scaling of config server, which is causing mongos rolling restart with each added member
        sc.assert_reaches_phase(Phase.Running, timeout=2400)

    def test_expected_processes_in_ac(self, sc: MongoDB):
        all_process_names = [p["name"] for p in sc.get_automation_config_tester().get_all_processes()]
        assert set(get_all_sharded_cluster_pod_names(sc)) == set(all_process_names)
