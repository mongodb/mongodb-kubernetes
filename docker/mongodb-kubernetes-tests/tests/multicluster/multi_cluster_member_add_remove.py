from typing import Callable, List

import kubernetes
import pytest
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import configure_multi_cluster_members
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    # ensure certs are created for the members during scale up
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
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
def mongodb_multi(mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str) -> MongoDBMulti:
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    mongodb_multi_unmarshalled.update()
    return mongodb_multi_unmarshalled


@pytest.mark.e2e_multi_cluster_recover
def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    # deploy the operator without the final cluster
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names[:-1])
    operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_recover
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_recover
def test_recover_operator_add_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_name: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    # Register the previously left-out member cluster.
    configure_multi_cluster_members([member_cluster_names[-1]], namespace, namespace, central_cluster_name)
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_recover
def test_mongodb_multi_recovers_adding_cluster(mongodb_multi: MongoDBMulti, member_cluster_names: List[str]):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"].append({"clusterName": member_cluster_names[-1], "members": 2})
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_multi_cluster_recover
def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    # The surviving set is member_cluster_names[1:], so de-register the first cluster: delete
    # its MemberCluster CR and credential Secret from the central cluster.
    removed_cluster_name = member_cluster_names[0]
    kubernetes.client.CustomObjectsApi(central_cluster_client).delete_namespaced_custom_object(
        group="operator.mongodb.com",
        version="v1",
        namespace=namespace,
        plural="memberclusters",
        name=removed_cluster_name,
    )
    kubernetes.client.CoreV1Api(api_client=central_cluster_client).delete_namespaced_secret(
        name=f"mck-credential-{removed_cluster_name}",
        namespace=namespace,
    )
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_recover
def test_mongodb_multi_recovers_removing_cluster(mongodb_multi: MongoDBMulti, member_cluster_names: List[str]):
    mongodb_multi.load()

    last_transition_time = mongodb_multi.get_status_last_transition_time()

    mongodb_multi["spec"]["clusterSpecList"].pop(0)
    mongodb_multi.update()
    mongodb_multi.assert_state_transition_happens(last_transition_time)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)
