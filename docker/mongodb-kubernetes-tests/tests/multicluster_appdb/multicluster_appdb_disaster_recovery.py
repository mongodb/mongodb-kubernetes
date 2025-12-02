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
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import (
    get_member_cluster_api_client,
)
from tests.constants import MULTI_CLUSTER_MEMBER_LIST_CONFIGMAP
from tests.multicluster.conftest import cluster_spec_list

FAILED_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-3"
OM_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-1"


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("multicluster_appdb_om.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version

    resource.allow_mdb_rc_versions()
    resource.create_admin_secret(api_client=central_cluster_client)

    resource["spec"]["backup"] = {"enabled": False}
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "clusterSpecList": cluster_spec_list(["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME], [3, 2]),
        "version": custom_appdb_version,
        "agent": {"logLevel": "DEBUG"},
        "security": {
            "certsSecretPrefix": "prefix",
            "tls": {"ca": multi_cluster_issuer_ca_configmap},
        },
    }

    return resource


@fixture(scope="module")
def appdb_certs_secret(
    namespace: str,
    multi_cluster_issuer: str,
    ops_manager: MongoDBOpsManager,
):
    return create_appdb_certs(
        namespace,
        multi_cluster_issuer,
        ops_manager.name + "-db",
        cluster_index_with_members=[(0, 5), (1, 5), (2, 5)],
        cert_prefix="prefix",
    )


@mark.e2e_multi_cluster_appdb_disaster_recovery
@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_patch_central_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    corev1 = kubernetes.client.CoreV1Api(api_client=central_cluster_client)
    ns = corev1.read_namespace(namespace)
    ns.metadata.labels["istio-injection"] = "enabled"
    corev1.patch_namespace(namespace, ns)


@fixture(scope="module")
def config_version():
    class ConfigVersion:
        version = 0

    return ConfigVersion()


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb_disaster_recovery
def test_create_om(ops_manager: MongoDBOpsManager, appdb_certs_secret: str, config_version):
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)

    config_version.version = ops_manager.get_automation_config_tester().automation_config["version"]


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_create_om_majority_down(ops_manager: MongoDBOpsManager, appdb_certs_secret: str, config_version):
    # failed cluster has majority members
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        ["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME], [2, 3]
    )

    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)

    config_version.version = ops_manager.get_automation_config_tester().automation_config["version"]


@mark.e2e_multi_cluster_appdb_disaster_recovery
@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_remove_cluster_from_operator_member_list_to_simulate_it_is_unhealthy(
    namespace, central_cluster_client: kubernetes.client.ApiClient
):
    member_list_cm = read_configmap(
        namespace,
        MULTI_CLUSTER_MEMBER_LIST_CONFIGMAP,
        api_client=central_cluster_client,
    )
    # this if is only for allowing re-running the test locally
    # without it the test function could be executed only once until the map is populated again by running prepare-local-e2e run again
    if FAILED_MEMBER_CLUSTER_NAME in member_list_cm:
        member_list_cm.pop(FAILED_MEMBER_CLUSTER_NAME)

    # this will trigger operators restart as it panics on changing the configmap
    update_configmap(
        namespace,
        MULTI_CLUSTER_MEMBER_LIST_CONFIGMAP,
        member_list_cm,
        api_client=central_cluster_client,
    )


@mark.e2e_multi_cluster_appdb_disaster_recovery
@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_delete_om_and_appdb_statefulset_in_failed_cluster(
    ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient
):
    appdb_sts_name = f"{ops_manager.name}-db-1"
    try:
        # delete OM to simulate losing Ops Manager application
        # this is only for testing unavailability of the OM application, it's not testing losing OM cluster
        # we don't delete here any additional resources (secrets, configmaps) that are required for a proper OM recovery testing
        # it will be immediately recreated by the operator, so we cannot check if it was deleted
        delete_statefulset(
            ops_manager.namespace,
            ops_manager.name,
            propagation_policy="Background",
            api_client=get_member_cluster_api_client(OM_MEMBER_CLUSTER_NAME),
        )
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise e

    try:
        # delete appdb statefulset in failed member cluster to simulate full cluster outage
        delete_statefulset(
            ops_manager.namespace,
            appdb_sts_name,
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

    run_periodically(
        lambda: statefulset_is_deleted(
            ops_manager.namespace,
            appdb_sts_name,
            api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
        ),
        timeout=120,
    )


@mark.e2e_multi_cluster_appdb_disaster_recovery
@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_appdb_is_stable_and_om_is_recreated(ops_manager: MongoDBOpsManager, config_version):
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)

    # there shouldn't be any automation config version change when one of the clusters is lost and OM is recreated
    current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
    assert current_ac_version == config_version.version


@mark.e2e_multi_cluster_appdb_disaster_recovery
def test_add_appdb_member_to_om_cluster(ops_manager: MongoDBOpsManager, config_version):
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        ["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME, OM_MEMBER_CLUSTER_NAME],
        [3, 2, 1],
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)

    # there should be exactly one automation config version change when we add new member
    current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
    assert current_ac_version == config_version.version + 1

    replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
    assert len(replica_set_members) == 3 + 2 + 1

    config_version.version = current_ac_version


@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_add_appdb_member_to_om_cluster_force_reconfig(ops_manager: MongoDBOpsManager, config_version):
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        ["kind-e2e-cluster-2", FAILED_MEMBER_CLUSTER_NAME, OM_MEMBER_CLUSTER_NAME],
        [3, 2, 1],
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Pending)

    ops_manager.reload()
    ops_manager["metadata"]["annotations"].update({"mongodb.com/v1.forceReconfigure": "true"})
    ops_manager.update()

    # This can potentially take quite a bit of time. AppDB needs to go up and sync with OM (which will be crashlooping)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)

    replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
    assert len(replica_set_members) == 3 + 2 + 1

    config_version.version = ops_manager.get_automation_config_tester().automation_config["version"]


@mark.e2e_multi_cluster_appdb_disaster_recovery
@mark.e2e_multi_cluster_appdb_disaster_recovery_force_reconfigure
def test_remove_failed_member_cluster_has_been_scaled_down(ops_manager: MongoDBOpsManager, config_version):
    # we remove failed member cluster
    # thanks to previous spec stored in the config map, the operator recognizes we need to scale its 2 processes one by one
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        ["kind-e2e-cluster-2", OM_MEMBER_CLUSTER_NAME], [3, 1]
    )
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
    ops_manager.om_status().assert_reaches_phase(Phase.Running)

    current_ac_version = ops_manager.get_automation_config_tester().automation_config["version"]
    assert current_ac_version == config_version.version + 2  # two scale downs

    replica_set_members = ops_manager.get_automation_config_tester().get_replica_set_members(f"{ops_manager.name}-db")
    assert len(replica_set_members) == 3 + 1
