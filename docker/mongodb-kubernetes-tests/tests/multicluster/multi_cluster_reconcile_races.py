# It's intended to check for reconcile data races.
import json
import time
from typing import List, Optional

import kubernetes.client
import pytest
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    find_fixture,
    read_service,
    try_load,
)
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.conftest import (
    get_central_cluster_client,
    get_custom_mdb_version,
    get_member_cluster_names,
    update_coredns_hosts,
)
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME, TELEMETRY_CONFIGMAP_NAME
from tests.multicluster.conftest import cluster_spec_list


@pytest.fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(find_fixture("om_validation.yaml"), namespace=namespace, name="om")

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    # Enable external connectivity so member clusters can reach OM
    resource["spec"]["externalConnectivity"] = {"type": "LoadBalancer"}

    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def om_external_base_domain(
    ops_manager: MongoDBOpsManager,
) -> str:
    interconnected_domain = f"om.{ops_manager.namespace}.interconnected"
    return interconnected_domain


@pytest.fixture(scope="module")
def om_external_base_url(ops_manager: MongoDBOpsManager, om_external_base_domain: str) -> str:
    """
    The base_url makes OM accessible from member clusters via a special interconnected dns address.
    This address only works for member clusters.
    """
    return f"http://{om_external_base_domain}:8080"


@pytest.fixture(scope="module")
def ops_manager2(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(find_fixture("om_validation.yaml"), namespace=namespace, name="om2")

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)
    return resource


def get_replica_set(ops_manager, namespace: str, idx: int) -> MongoDB:
    name = f"mdb-{idx}-rs"
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=name,
    ).configure(ops_manager, name, api_client=get_central_cluster_client())
    resource.set_version(get_custom_mdb_version())

    try_load(resource)
    return resource


def get_mdbmc(ops_manager, namespace: str, idx: int, om_external_base_url: str) -> MongoDBMulti:
    name = f"mdb-{idx}-mc"
    central_client = get_central_cluster_client()
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodbmulticluster-multi-cluster.yaml"),
        namespace=namespace,
        name=name,
    ).configure(ops_manager, name, api_client=central_client)
    resource.set_version(ensure_ent_version(get_custom_mdb_version()))
    resource.api = kubernetes.client.CustomObjectsApi(central_client)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

    # Update the configmap to use the external base URL so member clusters can reach OM
    config_map_name = f"{name}-config"
    config_data = KubernetesTester.read_configmap(namespace, config_map_name, api_client=central_client)
    config_data["baseUrl"] = om_external_base_url
    KubernetesTester.delete_configmap(namespace, config_map_name, api_client=central_client)
    create_or_update_configmap(namespace, config_map_name, config_data, api_client=central_client)

    try_load(resource)
    return resource


def get_sharded(ops_manager, namespace: str, idx: int) -> MongoDB:
    name = f"mdb-{idx}-sh"
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-single.yaml"),
        namespace=namespace,
        name=name,
    ).configure(ops_manager, name, api_client=get_central_cluster_client())
    resource.set_version(get_custom_mdb_version())

    try_load(resource)
    return resource


def get_standalone(ops_manager, namespace: str, idx: int) -> MongoDB:
    name = f"mdb-{idx}-st"
    resource = MongoDB.from_yaml(
        yaml_fixture("standalone.yaml"),
        namespace=namespace,
        name=name,
    ).configure(ops_manager, name, api_client=get_central_cluster_client())
    try_load(resource)
    return resource


def get_user(namespace: str, idx: int, mdb: MongoDB) -> MongoDBUser:
    name = f"{mdb.name}-user-{idx}"
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodb-user.yaml"),
        namespace=namespace,
        name=name,
    )
    try_load(resource)
    return resource


def get_all_sharded(ops_manager, namespace) -> list[MongoDB]:
    return [get_sharded(ops_manager, namespace, idx) for idx in range(0, 4)]


def get_all_rs(ops_manager, namespace) -> list[MongoDB]:
    return [get_replica_set(ops_manager, namespace, idx) for idx in range(0, 5)]


def get_all_mdbmc(ops_manager, namespace, om_external_base_url: str) -> list[MongoDB]:
    return [get_mdbmc(ops_manager, namespace, idx, om_external_base_url) for idx in range(0, 4)]


def get_all_standalone(ops_manager, namespace) -> list[MongoDB]:
    return [get_standalone(ops_manager, namespace, idx) for idx in range(0, 5)]


def get_all_users(namespace, mdb: MongoDB) -> list[MongoDBUser]:
    return [get_user(namespace, idx, mdb) for idx in range(0, 2)]


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_create_om(ops_manager: MongoDBOpsManager, ops_manager2: MongoDBOpsManager):
    ops_manager.update()
    ops_manager2.update()


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_om_ready(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_setup_om_external_connectivity(
    ops_manager: MongoDBOpsManager,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    om_external_base_url: str,
    om_external_base_domain: str,
):
    """
    Set up external connectivity for Ops Manager so that MongoDBMulti pods
    in member clusters can reach OM to download the agent binaries.
    """

    ops_manager.load()
    external_svc_name = ops_manager.external_svc_name()
    svc = read_service(ops_manager.namespace, external_svc_name, api_client=central_cluster_client)

    # Get the external IP from the LoadBalancer service
    ip = svc.status.load_balancer.ingress[0].ip

    # Update CoreDNS in each member cluster to resolve the interconnected domain to the OM external IP
    for c in member_cluster_clients:
        update_coredns_hosts(
            host_mappings=[(ip, om_external_base_domain)],
            api_client=c.api_client,
            cluster_name=c.cluster_name,
        )

    # Also update CoreDNS in the central cluster for consistency
    update_coredns_hosts(
        host_mappings=[(ip, om_external_base_domain)],
        api_client=central_cluster_client,
        cluster_name="central-cluster",
    )

    # Update OM's centralUrl to use the external address so agents communicate correctly
    ops_manager["spec"]["configuration"] = ops_manager["spec"].get("configuration", {})
    ops_manager["spec"]["configuration"]["mms.centralUrl"] = om_external_base_url
    ops_manager.update()

    # Wait for OM to reconcile with the new configuration
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_om2_ready(ops_manager2: MongoDBOpsManager):
    ops_manager2.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
    ops_manager2.om_status().assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_create_mdb(ops_manager: MongoDBOpsManager, namespace: str):
    for resource in get_all_rs(ops_manager, namespace):
        resource["spec"]["security"] = {
            "authentication": {"agents": {"mode": "SCRAM"}, "enabled": True, "modes": ["SCRAM"]}
        }
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_create_mdbmc(ops_manager: MongoDBOpsManager, namespace: str, om_external_base_url: str):
    for resource in get_all_mdbmc(ops_manager, namespace, om_external_base_url):
        resource.update()
    for r in get_all_mdbmc(ops_manager, namespace, om_external_base_url):
        r.assert_reaches_phase(Phase.Running, timeout=1600)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_create_sharded(ops_manager: MongoDBOpsManager, namespace: str):
    for resource in get_all_sharded(ops_manager, namespace):
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_sharded(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_create_standalone(ops_manager: MongoDBOpsManager, namespace: str):
    for resource in get_all_standalone(ops_manager, namespace):
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_standalone(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_create_users(ops_manager: MongoDBOpsManager, namespace: str):
    create_or_update_secret(
        namespace,
        "mdb-user-password",
        {"password": "password"},
    )
    for mdb in get_all_rs(ops_manager, namespace):
        for resource in get_all_users(namespace, mdb):
            resource["spec"]["mongodbResourceRef"] = {"name": mdb.name}
            resource["spec"]["passwordSecretKeyRef"] = {"name": "mdb-user-password", "key": "password"}
            resource.update()

    for r in get_all_rs(ops_manager, namespace):
        for resource in get_all_users(namespace, mdb):
            resource.assert_reaches_phase(Phase.Updated, timeout=400)
        r.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_pod_logs_race(multi_cluster_operator: Operator):
    pods = multi_cluster_operator.list_operator_pods()
    pod_name = pods[0].metadata.name
    container_name = MULTI_CLUSTER_OPERATOR_NAME
    pod_logs_str = KubernetesTester.read_pod_logs(
        multi_cluster_operator.namespace, pod_name, container_name, api_client=multi_cluster_operator.api_client
    )
    contains_race = "WARNING: DATA RACE" in pod_logs_str
    assert not contains_race


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_restart_operator_pod(
    ops_manager: MongoDBOpsManager, namespace: str, multi_cluster_operator: Operator, om_external_base_url: str
):
    # this enforces a requeue of all existing resources, increasing the chances of races to happen
    multi_cluster_operator.restart_operator_deployment()
    multi_cluster_operator.assert_is_running()
    time.sleep(5)
    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)
    for r in get_all_mdbmc(ops_manager, namespace, om_external_base_url):
        r.assert_reaches_phase(Phase.Running)
    for r in get_all_sharded(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)
    for r in get_all_standalone(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_pod_logs_race_after_restart(multi_cluster_operator: Operator):
    pods = multi_cluster_operator.list_operator_pods()
    pod_name = pods[0].metadata.name
    container_name = MULTI_CLUSTER_OPERATOR_NAME
    pod_logs_str = KubernetesTester.read_pod_logs(
        multi_cluster_operator.namespace, pod_name, container_name, api_client=multi_cluster_operator.api_client
    )
    contains_race = "WARNING: DATA RACE" in pod_logs_str
    assert not contains_race


@pytest.mark.e2e_om_reconcile_race_with_telemetry
def test_telemetry_configmap(namespace: str):
    config = KubernetesTester.read_configmap(namespace, TELEMETRY_CONFIGMAP_NAME)
    for ts_key in ["lastSendTimestampClusters", "lastSendTimestampDeployments", "lastSendTimestampOperators"]:
        ts_cm = config.get(ts_key)
        assert ts_cm.isdigit()  # it should be a timestamp

    for ps_key in ["lastSendPayloadClusters", "lastSendPayloadDeployments", "lastSendPayloadOperators"]:
        try:
            payload_string = config.get(ps_key)
            payload = json.loads(payload_string)
            # Perform a rudimentary check
            assert isinstance(payload, list), "payload should be a list"
            assert len(payload) > 0, "payload should not be empty"
        except json.JSONDecodeError:
            pytest.fail("payload contains invalid JSON data")
