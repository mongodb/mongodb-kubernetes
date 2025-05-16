import os
import time
from typing import Dict, List

import kubernetes
from kubernetes import client
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import (
    MULTI_CLUSTER_OPERATOR_NAME,
    _install_multi_cluster_operator,
    run_kube_config_creation_tool,
)

from . import prepare_multi_cluster_namespaces
from .conftest import cluster_spec_list, create_namespace


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
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", mdba_ns)
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
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", mdbb_ns)
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
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", unmanaged_mdb_ns)

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


@mark.e2e_multi_cluster_specific_namespaces
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
    image_pull_secret_name = multi_cluster_operator_installation_config["registry.imagePullSecrets"]
    image_pull_secret_data = read_secret(namespace, image_pull_secret_name)

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        mdba_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        mdbb_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        unmanaged_mdb_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )


@mark.e2e_multi_cluster_specific_namespaces
def test_prepare_namespace(
    multi_cluster_operator_installation_config: Dict[str, str],
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_name: str,
    mdba_ns: str,
    mdbb_ns: str,
):
    prepare_multi_cluster_namespaces(
        mdba_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
    )

    prepare_multi_cluster_namespaces(
        mdbb_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
    )


@mark.e2e_multi_cluster_clusterwide
def test_deploy_operator(multi_cluster_operator_clustermode: Operator):
    multi_cluster_operator_clustermode.assert_is_running()


@mark.e2e_multi_cluster_specific_namespaces
def test_deploy_operator(install_operator: Operator):
    install_operator.assert_is_running()


@mark.e2e_multi_cluster_specific_namespaces
def test_copy_configmap_and_secret_across_ns(
    namespace: str,
    central_cluster_client: client.ApiClient,
    multi_cluster_operator_installation_config: Dict[str, str],
    mdba_ns: str,
    mdbb_ns: str,
):
    data = KubernetesTester.read_configmap(namespace, "my-project", api_client=central_cluster_client)
    data["projectName"] = mdba_ns
    create_or_update_configmap(mdba_ns, "my-project", data, api_client=central_cluster_client)

    data["projectName"] = mdbb_ns
    create_or_update_configmap(mdbb_ns, "my-project", data, api_client=central_cluster_client)

    data = read_secret(namespace, "my-credentials", api_client=central_cluster_client)
    create_or_update_secret(mdba_ns, "my-credentials", data, api_client=central_cluster_client)
    create_or_update_secret(mdbb_ns, "my-credentials", data, api_client=central_cluster_client)


@mark.e2e_multi_cluster_specific_namespaces
def test_create_mongodb_multi_nsa(mongodb_multi_a: MongoDBMulti):
    mongodb_multi_a.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_specific_namespaces
def test_create_mongodb_multi_nsb(mongodb_multi_b: MongoDBMulti):
    mongodb_multi_b.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_specific_namespaces
def test_create_mongodb_multi_unmanaged(unmanaged_mongodb_multi: MongoDBMulti):
    """
    For an unmanaged resource, the status should not be updated!
    """
    for i in range(10):
        time.sleep(5)

        unmanaged_mongodb_multi.reload()
        assert "status" not in unmanaged_mongodb_multi
