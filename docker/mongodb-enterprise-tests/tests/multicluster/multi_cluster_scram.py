import kubernetes

import pytest
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester import create_secret

MDB_RESOURCE = "multi-replica-set-scram"
USER_NAME = "my-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace
    )

    resource["spec"]["security"] = {
        "authentication": {"enabled": True, "modes": ["SCRAM"]}
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@pytest.fixture(scope="module")
def mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodb-user.yaml"), "multi-replica-set-scram-user", namespace
    )

    resource["spec"]["username"] = USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {
        "name": PASSWORD_SECRET_NAME,
        "key": "password",
    }
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@pytest.mark.e2e_multi_cluster_scram
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_scram
def test_create_mongodb_multi_with_scram(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=500)


@pytest.mark.e2e_multi_cluster_scram
def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    # create user secret first
    create_secret(
        namespace=namespace,
        name=PASSWORD_SECRET_NAME,
        data={"password": USER_PASSWORD},
        api_client=central_cluster_client,
    )
    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=100)


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
    tester.assert_authentication_enabled()
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
