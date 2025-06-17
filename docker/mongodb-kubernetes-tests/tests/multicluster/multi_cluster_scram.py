from typing import List

import kubernetes
import pytest
from kubetester import create_or_update_secret, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import with_scram
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE = "multi-replica-set-scram"
USER_NAME = "my-user-1"
USER_RESOURCE = "multi-replica-set-scram-user"
USER_DATABASE = "admin"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"
NEW_USER_PASSWORD = "my-new-password7"


@pytest.fixture(scope="function")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
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


@pytest.mark.e2e_multi_cluster_scram
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_scram
def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    # create user secret first
    create_or_update_secret(
        namespace=namespace,
        name=PASSWORD_SECRET_NAME,
        data={"password": USER_PASSWORD},
        api_client=central_cluster_client,
    )
    mongodb_user.update()
    mongodb_user.assert_reaches_phase(Phase.Pending, timeout=100)


@pytest.mark.e2e_multi_cluster_scram
def test_create_mongodb_multi_with_scram(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@pytest.mark.e2e_multi_cluster_scram
def test_user_reaches_updated(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
):
    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=100)


@pytest.mark.e2e_multi_cluster_scram
def test_replica_set_connectivity_using_user_password(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(db="admin", opts=[with_scram(USER_NAME, USER_PASSWORD)])


@pytest.mark.e2e_multi_cluster_scram
def test_change_password_and_check_connectivity(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    central_cluster_client: kubernetes.client.ApiClient,
):
    create_or_update_secret(
        namespace,
        PASSWORD_SECRET_NAME,
        {"password": NEW_USER_PASSWORD},
        api_client=central_cluster_client,
    )
    tester = mongodb_multi.tester()
    tester.assert_scram_sha_authentication(
        password=NEW_USER_PASSWORD,
        username=USER_NAME,
        auth_mechanism="SCRAM-SHA-256",
    )


@pytest.mark.e2e_multi_cluster_scram
def test_user_cannot_authenticate_with_old_password(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_scram_sha_authentication_fails(
        password=USER_PASSWORD,
        username=USER_NAME,
        auth_mechanism="SCRAM-SHA-256",
    )


@pytest.mark.e2e_multi_cluster_scram
def test_connection_string_secret_was_created(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    for client in member_cluster_clients:
        secret_data = read_secret(
            namespace,
            f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
            api_client=client.api_client,
        )
        assert "username" in secret_data
        assert "password" in secret_data
        assert "connectionString.standard" in secret_data
        assert "connectionString.standardSrv" in secret_data


@pytest.mark.e2e_multi_cluster_scram
def test_om_configured_correctly():
    expected_roles = {
        ("admin", "clusterAdmin"),
        ("admin", "userAdminAnyDatabase"),
        ("admin", "readWrite"),
        ("admin", "userAdminAnyDatabase"),
    }
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_has_user(USER_NAME)
    tester.assert_user_has_roles(USER_NAME, expected_roles)
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=3)
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-1", active_auth_mechanism=False)
    tester.assert_authentication_mechanism_enabled("MONGODB-CR", active_auth_mechanism=False)


@pytest.mark.e2e_multi_cluster_scram
def test_replica_set_connectivity(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(db="admin", opts=[with_scram(USER_NAME, NEW_USER_PASSWORD)])


@pytest.mark.e2e_multi_cluster_scram
def test_replica_set_connectivity_from_connection_string_standard(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    secret_data = read_secret(
        namespace,
        f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
        api_client=member_cluster_clients[-1].api_client,
    )
    tester = mongodb_multi.tester()
    tester.cnx_string = secret_data["connectionString.standard"]
    tester.assert_connectivity(
        db="admin",
        opts=[with_scram(USER_NAME, NEW_USER_PASSWORD)],
    )


@pytest.mark.e2e_multi_cluster_scram
def test_replica_set_connectivity_from_connection_string_standard_srv(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    secret_data = read_secret(
        namespace,
        f"{mongodb_multi.name}-{USER_RESOURCE}-{USER_DATABASE}",
        api_client=member_cluster_clients[-1].api_client,
    )
    tester = mongodb_multi.tester()
    tester.cnx_string = secret_data["connectionString.standardSrv"]
    tester.assert_connectivity(
        db="admin",
        opts=[
            with_scram(USER_NAME, NEW_USER_PASSWORD),
        ],
    )
