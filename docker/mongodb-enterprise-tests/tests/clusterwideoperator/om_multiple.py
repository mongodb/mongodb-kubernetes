from kubetester.kubetester import fixture as yaml_fixture, create_testing_namespace
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from kubetester import (
    create_service_account,
    create_object_from_dict,
    create_secret,
    read_secret,
)
import yaml
from typing import Dict


def create_role(namespace: str) -> str:
    with open(yaml_fixture("role.yaml")) as f:
        role_dict = yaml.safe_load(f)

    return create_object_from_dict(role_dict, namespace)


def create_role_binding(namespace: str) -> str:

    with open(yaml_fixture("rolebinding.yaml")) as f:
        rolebinding_dict = yaml.safe_load(f)

    rolebinding_dict["subjects"][0]["namespace"] = namespace
    return create_object_from_dict(rolebinding_dict, namespace)


def ops_manager(
    namespace: str,
) -> MongoDBOpsManager:

    data = dict(
        Username="test-user",
        Password="@Sihjifutestpass21nnH",
        FirstName="foo",
        LastName="bar",
    )

    create_service_account(namespace, "mongodb-enterprise-appdb"),
    create_service_account(namespace, "mongodb-enterprise-ops-manager"),
    create_secret(namespace=namespace, name="ops-manager-admin-secret", data=data),
    create_role(namespace),
    create_role_binding(namespace),

    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )

    return om


@fixture(scope="module")
def om1() -> MongoDBOpsManager:
    om = ops_manager("om-1")
    return om.create()


@fixture(scope="module")
def om2() -> MongoDBOpsManager:
    om = ops_manager("om-2")
    return om.create()


@mark.e2e_om_multiple
def test_install_operator(operator_clusterwide: Operator):
    operator_clusterwide.assert_is_running()


@mark.e2e_om_multiple
def test_create_namespaces(evergreen_task_id: str):
    create_testing_namespace(evergreen_task_id, "om-1")
    create_testing_namespace(evergreen_task_id, "om-2")


@mark.e2e_om_multiple
def test_create_image_pull_secret_om1(
    namespace: str, operator_installation_config: Dict[str, str]
):
    """ We need to copy image pull secrets to om namespace """
    secret_name = operator_installation_config["registry.imagePullSecrets"]
    data = read_secret(namespace, secret_name)
    create_secret("om-1", secret_name, data, type="kubernetes.io/dockerconfigjson")


@mark.e2e_om_multiple
def test_multiple_om_created_1(om1: MongoDBOpsManager):
    om1.om_status().assert_reaches_phase(Phase.Running, timeout=1100)


@mark.e2e_om_multiple
def test_create_image_pull_secret_om2(
    namespace: str, operator_installation_config: Dict[str, str]
):
    """ We need to copy image pull secrets to om namespace """
    secret_name = operator_installation_config["registry.imagePullSecrets"]
    data = read_secret(namespace, secret_name)
    create_secret("om-2", secret_name, data, type="kubernetes.io/dockerconfigjson")


@mark.e2e_om_multiple
def test_multiple_om_created_2(om2: MongoDBOpsManager):
    om2.om_status().assert_reaches_phase(Phase.Running, timeout=1100)
