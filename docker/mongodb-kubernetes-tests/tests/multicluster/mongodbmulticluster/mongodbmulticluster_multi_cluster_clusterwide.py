import os
from typing import Dict, List

import kubernetes
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import (
    _install_multi_cluster_operator,
    run_kube_config_creation_tool,
)
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_clusterwide as testhelper

MDB_RESOURCE = "multi-replica-set"


@fixture(scope="module")
def mdba_ns(namespace: str):
    return "{}-mdb-ns-a".format(namespace)


@fixture(scope="module")
def mdbb_ns(namespace: str):
    return "{}-mdb-ns-b".format(namespace)


@fixture(scope="module")
def unmanaged_mdb_ns(namespace: str):
    return "{}-mdb-ns-c".format(namespace)


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
def unmanaged_mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    unmanaged_mdb_ns: str,
    member_cluster_names: List[str],
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, unmanaged_mdb_ns)

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
    member_cluster_clients: List[kubernetes.client.ApiClient],
    cluster_clients: Dict[str, kubernetes.client.ApiClient],
    member_cluster_names: List[str],
    mdba_ns: str,
    mdbb_ns: str,
) -> Operator:
    print(f"Installing operator in context: {central_cluster_name}")
    os.environ["HELM_KUBECONTEXT"] = central_cluster_name
    member_cluster_namespaces = mdba_ns + "," + mdbb_ns
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names, True)

    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.createOperatorServiceAccount": "false",
            "operator.watchNamespace": member_cluster_namespaces,
        },
        central_cluster_name,
    )


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
def test_create_namespaces(
    namespace: str,
    mdba_ns: str,
    mdbb_ns: str,
    unmanaged_mdb_ns: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
    multi_cluster_operator_installation_config: Dict[str, str],
):
    testhelper.test_create_namespaces(
        namespace,
        mdba_ns,
        mdbb_ns,
        unmanaged_mdb_ns,
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        multi_cluster_operator_installation_config,
    )


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
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


@mark.e2e_mongodbmulticluster_multi_cluster_clusterwide
def test_deploy_operator(multi_cluster_operator_clustermode: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator_clustermode)


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
def test_deploy_operator(install_operator: Operator):
    testhelper.test_deploy_operator(install_operator)


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
def test_copy_configmap_and_secret_across_ns(
    namespace: str,
    central_cluster_client: client.ApiClient,
    multi_cluster_operator_installation_config: Dict[str, str],
    mdba_ns: str,
    mdbb_ns: str,
):
    testhelper.test_copy_configmap_and_secret_across_ns(
        namespace, central_cluster_client, multi_cluster_operator_installation_config, mdba_ns, mdbb_ns
    )


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
def test_create_mongodb_multi_nsa(mongodb_multi_a: MongoDBMulti):
    testhelper.test_create_mongodb_multi_nsa(mongodb_multi_a)


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
def test_create_mongodb_multi_nsb(mongodb_multi_b: MongoDBMulti):
    testhelper.test_create_mongodb_multi_nsb(mongodb_multi_b)


@mark.e2e_mongodbmulticluster_multi_cluster_specific_namespaces
def test_create_mongodb_multi_unmanaged(unmanaged_mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi_unmanaged(unmanaged_mongodb_multi)
