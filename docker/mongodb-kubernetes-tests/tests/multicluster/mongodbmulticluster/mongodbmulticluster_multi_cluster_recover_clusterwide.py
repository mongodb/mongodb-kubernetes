import os
from typing import Dict, List

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import (
    MULTI_CLUSTER_OPERATOR_NAME,
    OPERATOR_NAME,
    _install_multi_cluster_operator,
    run_kube_config_creation_tool,
)
from tests.multicluster.conftest import (
    cluster_spec_list,
)

from ..shared import multi_cluster_recover_clusterwide as testhelper

MDB_RESOURCE = "multi-replica-set"


@fixture(scope="module")
def mdba_ns(namespace: str):
    return "{}-mdb-ns-a".format(namespace)


@fixture(scope="module")
def mdbb_ns(namespace: str):
    return "{}-mdb-ns-b".format(namespace)


@fixture(scope="module")
def mongodb_multi_a(
    central_cluster_client: kubernetes.client.ApiClient,
    mdba_ns: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, mdba_ns)
    resource.set_version(custom_mdb_version)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@fixture(scope="module")
def mongodb_multi_b(
    central_cluster_client: kubernetes.client.ApiClient,
    mdbb_ns: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, mdbb_ns)
    resource.set_version(custom_mdb_version)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@fixture(scope="module")
def install_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
    mdba_ns: str,
    mdbb_ns: str,
) -> Operator:
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    member_cluster_namespaces = mdba_ns + "," + mdbb_ns
    run_kube_config_creation_tool(
        member_cluster_names,
        namespace,
        namespace,
        member_cluster_names,
        True,
        service_account_name=MULTI_CLUSTER_OPERATOR_NAME,
        operator_name=OPERATOR_NAME,
    )

    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.deployment_name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.createOperatorServiceAccount": "false",
            "operator.watchNamespace": member_cluster_namespaces,
            "multiCluster.performFailOver": "false",
        },
        central_cluster_name,
        operator_name=MULTI_CLUSTER_OPERATOR_NAME,
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_label_operator_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    testhelper.test_label_operator_namespace(namespace, central_cluster_client)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_create_namespaces(
    namespace: str,
    mdba_ns: str,
    mdbb_ns: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
    multi_cluster_operator_installation_config: Dict[str, str],
):
    testhelper.test_create_namespaces(
        namespace,
        mdba_ns,
        mdbb_ns,
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        multi_cluster_operator_installation_config,
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_create_service_entry(service_entries: List[CustomObject]):
    testhelper.test_create_service_entry(service_entries)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_delete_cluster_role_and_binding(
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_delete_cluster_role_and_binding(central_cluster_client, member_cluster_clients)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_deploy_operator(install_operator: Operator):
    testhelper.test_deploy_operator(install_operator)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_prepare_namespace(
    multi_cluster_operator_installation_config: Dict[str, str],
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_name: str,
    mdba_ns: str,
    mdbb_ns: str,
):
    testhelper.test_prepare_namespace(
        multi_cluster_operator_installation_config, member_cluster_clients, central_cluster_name, mdba_ns, mdbb_ns
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_copy_configmap_and_secret_across_ns(
    namespace: str,
    central_cluster_client: client.ApiClient,
    mdba_ns: str,
    mdbb_ns: str,
):
    testhelper.test_copy_configmap_and_secret_across_ns(namespace, central_cluster_client, mdba_ns, mdbb_ns)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_create_mongodb_multi_nsa_nsb(mongodb_multi_a: MongoDBMulti, mongodb_multi_b: MongoDBMulti):
    testhelper.test_create_mongodb_multi_nsa_nsb(mongodb_multi_a, mongodb_multi_b)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_update_service_entry_block_failed_cluster_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):
    testhelper.test_update_service_entry_block_failed_cluster_traffic(
        namespace, central_cluster_client, member_cluster_names
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_delete_database_statefulsets_in_failed_cluster(
    mongodb_multi_a: MongoDBMulti,
    mongodb_multi_b: MongoDBMulti,
    mdba_ns: str,
    mdbb_ns: str,
    member_cluster_names: list[str],
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_delete_database_statefulsets_in_failed_cluster(
        mongodb_multi_a, mongodb_multi_b, mdba_ns, mdbb_ns, member_cluster_names, member_cluster_clients
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_mongodb_multi_nsa_enters_failed_stated(mongodb_multi_a: MongoDBMulti):
    testhelper.test_mongodb_multi_nsa_enters_failed_stated(mongodb_multi_a)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_mongodb_multi_nsb_enters_failed_stated(mongodb_multi_b: MongoDBMulti):
    testhelper.test_mongodb_multi_nsb_enters_failed_stated(mongodb_multi_b)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    mdba_ns: str,
    mdbb_ns: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    testhelper.test_recover_operator_remove_cluster(
        member_cluster_names, namespace, mdba_ns, mdbb_ns, central_cluster_client
    )


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_mongodb_multi_nsa_recovers_removing_cluster(mongodb_multi_a: MongoDBMulti):
    testhelper.test_mongodb_multi_nsa_recovers_removing_cluster(mongodb_multi_a)


@mark.e2e_mongodbmulticluster_multi_cluster_recover_clusterwide
def test_mongodb_multi_nsb_recovers_removing_cluster(mongodb_multi_b: MongoDBMulti):
    testhelper.test_mongodb_multi_nsb_recovers_removing_cluster(mongodb_multi_b)
