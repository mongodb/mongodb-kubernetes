from typing import List, Callable

import kubernetes
import pytest
from kubernetes import client

from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import (
    MongoDBMulti,
    MultiClusterClient,
)
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
)
from tests.conftest import (
    run_kube_config_creation_tool,
    run_multi_cluster_recovery_tool,
    MULTI_CLUSTER_OPERATOR_NAME,
)

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace
    )
    # ensure certs are created for the members during scale up
    resource["spec"]["clusterSpecList"][0]["members"] = 2
    resource["spec"]["clusterSpecList"][1]["members"] = 1
    resource["spec"]["clusterSpecList"][2]["members"] = 2
    resource["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@pytest.fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@pytest.fixture(scope="module")
def mongodb_multi(
    mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str
) -> MongoDBMulti:
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    return mongodb_multi_unmarshalled.create()


@pytest.mark.e2e_multi_cluster_recover
def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    run_kube_config_creation_tool(member_cluster_names[:-1], namespace, namespace)
    # deploy the operator without the final cluster
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names[:-1])
    operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_recover
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_recover
def test_recover_operator_add_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return_code = run_multi_cluster_recovery_tool(
        member_cluster_names, namespace, namespace
    )
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_recover
def test_mongodb_multi_recovers_adding_cluster(
    mongodb_multi: MongoDBMulti, member_cluster_names: List[str]
):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"].append(
        {"clusterName": member_cluster_names[-1], "members": 2}
    )
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_recover
def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return_code = run_multi_cluster_recovery_tool(
        member_cluster_names[1:], namespace, namespace
    )
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_recover
def test_mongodb_multi_recovers_removing_cluster(
    mongodb_multi: MongoDBMulti, member_cluster_names: List[str]
):
    mongodb_multi.load()

    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)

    mongodb_multi["spec"]["clusterSpecList"].pop(0)
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)
