from typing import List

import kubernetes
import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_scram as testhelper

MDB_RESOURCE = "multi-replica-set-scram"
USER_NAME = "my-user-1"
USER_RESOURCE = "multi-replica-set-scram-user"
PASSWORD_SECRET_NAME = "mms-user-1-password"


@pytest.fixture(scope="function")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)

    resource["spec"]["security"] = {
        "authentication": {
            "agents": {"mode": "MONGODB-CR"},
            "enabled": True,
            "modes": ["SCRAM-SHA-1", "SCRAM-SHA-256", "MONGODB-CR"],
        }
    }

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


@pytest.fixture(scope="function")
def mongodb_user(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("scram-sha-user.yaml"), USER_RESOURCE, namespace)

    resource["spec"]["username"] = USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {
        "name": PASSWORD_SECRET_NAME,
        "key": "password",
    }
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    testhelper.test_create_mongodb_user(central_cluster_client, mongodb_user, namespace)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_create_mongodb_multi_with_scram(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi_with_scram(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_user_reaches_updated(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
):
    testhelper.test_user_reaches_updated(central_cluster_client, mongodb_user)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_replica_set_connectivity_using_user_password(mongodb_multi: MongoDB):
    testhelper.test_replica_set_connectivity_using_user_password(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_change_password_and_check_connectivity(
    namespace: str,
    mongodb_multi: MongoDB,
    central_cluster_client: kubernetes.client.ApiClient,
):
    testhelper.test_change_password_and_check_connectivity(namespace, mongodb_multi, central_cluster_client)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_user_cannot_authenticate_with_old_password(mongodb_multi: MongoDB):
    testhelper.test_user_cannot_authenticate_with_old_password(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_connection_string_secret_was_created(
    namespace: str,
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_connection_string_secret_was_created(namespace, mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_om_configured_correctly():
    testhelper.test_om_configured_correctly()


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_replica_set_connectivity(mongodb_multi: MongoDB):
    testhelper.test_replica_set_connectivity(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_replica_set_connectivity_from_connection_string_standard(
    namespace: str,
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_replica_set_connectivity_from_connection_string_standard(
        namespace, mongodb_multi, member_cluster_clients
    )


@pytest.mark.e2e_mongodb_multi_cluster_scram
def test_replica_set_connectivity_from_connection_string_standard_srv(
    namespace: str,
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_replica_set_connectivity_from_connection_string_standard_srv(
        namespace, mongodb_multi, member_cluster_clients
    )
