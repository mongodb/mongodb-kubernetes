# It's intended to check for reconcile data races.
from typing import Optional

import kubernetes.client
import pytest
from kubetester import find_fixture, try_load
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager

from ..shared import multi_cluster_reconcile_races as testhelper


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

    try_load(resource)
    return resource


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


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_create_om(ops_manager: MongoDBOpsManager, ops_manager2: MongoDBOpsManager):
    testhelper.test_create_om(ops_manager, ops_manager2)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_om_ready(ops_manager: MongoDBOpsManager):
    testhelper.test_om_ready(ops_manager)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_om2_ready(ops_manager2: MongoDBOpsManager):
    testhelper.test_om2_ready(ops_manager2)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_create_mdb(ops_manager: MongoDBOpsManager, namespace: str):
    testhelper.test_create_mdb(ops_manager, namespace)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_create_mdbmc(ops_manager: MongoDBOpsManager, namespace: str):
    testhelper.test_create_mdbmc(ops_manager, "mongodbmulticluster", namespace)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_create_sharded(ops_manager: MongoDBOpsManager, namespace: str):
    testhelper.test_create_sharded(ops_manager, namespace)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_create_standalone(ops_manager: MongoDBOpsManager, namespace: str):
    testhelper.test_create_standalone(ops_manager, namespace)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_create_users(ops_manager: MongoDBOpsManager, namespace: str):
    testhelper.test_create_users(ops_manager, namespace)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_pod_logs_race(multi_cluster_operator: Operator):
    testhelper.test_pod_logs_race(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_restart_operator_pod(ops_manager: MongoDBOpsManager, namespace: str, multi_cluster_operator: Operator):
    testhelper.test_restart_operator_pod(ops_manager, namespace, multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_pod_logs_race_after_restart(multi_cluster_operator: Operator):
    testhelper.test_pod_logs_race_after_restart(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_om_reconcile_race_with_telemetry
def test_telemetry_configmap(namespace: str):
    testhelper.test_pod_logs_race_after_restart(namespace)
