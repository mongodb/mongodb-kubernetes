# It's intended to check for reconcile data races.
import json
import time

import pytest
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.conftest import (
    MULTI_CLUSTER_OPERATOR_NAME,
    TELEMETRY_CONFIGMAP_NAME,
    get_central_cluster_client,
    get_custom_mdb_version,
    get_member_cluster_names,
)
from tests.multicluster.conftest import cluster_spec_list


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


def get_mdbmc(ops_manager, type: str, namespace: str, idx: int) -> MongoDBMulti | MongoDB:
    name = f"mdb-{idx}-mc"
    resourceName = f"{type}-multi-cluster.yaml"
    if type == "mongodb":
        resource = MongoDB.from_yaml(
            yaml_fixture(resourceName),
            namespace=namespace,
            name=name,
        ).configure(ops_manager, name, api_client=get_central_cluster_client())
    else:
        resource = MongoDBMulti.from_yaml(
            yaml_fixture(resourceName),
            namespace=namespace,
            name=name,
        ).configure(ops_manager, name, api_client=get_central_cluster_client())
    try_load(resource)
    return resource


def get_sharded(ops_manager, namespace: str, idx: int) -> MongoDB:
    name = f"mdb-{idx}-sh"
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-single.yaml"),
        namespace=namespace,
        name=name,
    ).configure(ops_manager, name, api_client=get_central_cluster_client())
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


def get_user(ops_manager, namespace: str, idx: int, mdb: MongoDB) -> MongoDBUser:
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


def get_all_mdbmc(ops_manager, type, namespace) -> list[MongoDB]:
    return [get_mdbmc(ops_manager, type, namespace, idx) for idx in range(0, 4)]


def get_all_standalone(ops_manager, namespace) -> list[MongoDB]:
    return [get_standalone(ops_manager, namespace, idx) for idx in range(0, 5)]


def get_all_users(ops_manager, namespace, mdb: MongoDB) -> list[MongoDBUser]:
    return [get_user(ops_manager, namespace, idx, mdb) for idx in range(0, 2)]


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_om(ops_manager: MongoDBOpsManager, ops_manager2: MongoDBOpsManager):
    ops_manager.update()
    ops_manager2.update()


def test_om_ready(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)


def test_om2_ready(ops_manager2: MongoDBOpsManager):
    ops_manager2.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
    ops_manager2.om_status().assert_reaches_phase(Phase.Running, timeout=1800)


def test_create_mdb(ops_manager: MongoDBOpsManager, namespace: str):
    for resource in get_all_rs(ops_manager, namespace):
        resource["spec"]["security"] = {
            "authentication": {"agents": {"mode": "SCRAM"}, "enabled": True, "modes": ["SCRAM"]}
        }
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


def test_create_mdbmc(ops_manager: MongoDBOpsManager, type: str, namespace: str):
    for resource in get_all_mdbmc(ops_manager, type, namespace):
        resource.set_version(get_custom_mdb_version())
        resource["spec"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


def test_create_sharded(ops_manager: MongoDBOpsManager, namespace: str):
    for resource in get_all_sharded(ops_manager, namespace):
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


def test_create_standalone(ops_manager: MongoDBOpsManager, namespace: str):
    for resource in get_all_standalone(ops_manager, namespace):
        resource.set_version(get_custom_mdb_version())
        resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


def test_create_users(ops_manager: MongoDBOpsManager, namespace: str):
    create_or_update_secret(
        namespace,
        "mdb-user-password",
        {"password": "password"},
    )
    for mdb in get_all_rs(ops_manager, namespace):
        for resource in get_all_users(ops_manager, namespace, mdb):
            resource["spec"]["mongodbResourceRef"] = {"name": mdb.name}
            resource["spec"]["passwordSecretKeyRef"] = {"name": "mdb-user-password", "key": "password"}
            resource.update()

    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


def test_pod_logs_race(multi_cluster_operator: Operator):
    pods = multi_cluster_operator.list_operator_pods()
    pod_name = pods[0].metadata.name
    container_name = MULTI_CLUSTER_OPERATOR_NAME
    pod_logs_str = KubernetesTester.read_pod_logs(
        multi_cluster_operator.namespace, pod_name, container_name, api_client=multi_cluster_operator.api_client
    )
    contains_race = "WARNING: DATA RACE" in pod_logs_str
    assert not contains_race


def test_restart_operator_pod(ops_manager: MongoDBOpsManager, namespace: str, multi_cluster_operator: Operator):
    # this enforces a requeue of all existing resources, increasing the chances of races to happen
    multi_cluster_operator.restart_operator_deployment()
    multi_cluster_operator.assert_is_running()
    time.sleep(5)
    for r in get_all_rs(ops_manager, namespace):
        r.assert_reaches_phase(Phase.Running)


def test_pod_logs_race_after_restart(multi_cluster_operator: Operator):
    pods = multi_cluster_operator.list_operator_pods()
    pod_name = pods[0].metadata.name
    container_name = MULTI_CLUSTER_OPERATOR_NAME
    pod_logs_str = KubernetesTester.read_pod_logs(
        multi_cluster_operator.namespace, pod_name, container_name, api_client=multi_cluster_operator.api_client
    )
    contains_race = "WARNING: DATA RACE" in pod_logs_str
    assert not contains_race


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
