from kubetester.kubetester import fixture as yaml_fixture, create_testing_namespace
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import helm_template
from kubernetes import client
from pytest import fixture, mark
from kubetester import (
    create_service_account,
    create_object_from_dict,
    create_secret,
    read_secret,
)

from typing import Dict


def _prepare_om_namespace(
    ops_manager_namespace: str, operator_installation_config: Dict[str, str]
):
    """ create a new namespace and configures all necessary service accounts there """
    yaml_file = helm_template(
        helm_args={
            "namespace": ops_manager_namespace,
            "registry.imagePullSecrets": operator_installation_config[
                "registry.imagePullSecrets"
            ],
        },
        templates="templates/database-roles.yaml",
    )

    data = dict(
        Username="test-user",
        Password="@Sihjifutestpass21nnH",
        FirstName="foo",
        LastName="bar",
    )

    create_or_replace_from_yaml(client.api_client.ApiClient(), yaml_file)
    create_secret(
        namespace=ops_manager_namespace, name="ops-manager-admin-secret", data=data
    ),


def ops_manager(
    namespace: str, operator_installation_config: Dict[str, str]
) -> MongoDBOpsManager:
    _prepare_om_namespace(namespace, operator_installation_config)
    return MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )


@fixture(scope="module")
def om1(operator_installation_config: Dict[str, str]) -> MongoDBOpsManager:
    om = ops_manager("om-1", operator_installation_config)
    return om.create()


@fixture(scope="module")
def om2(operator_installation_config: Dict[str, str]) -> MongoDBOpsManager:
    om = ops_manager("om-2", operator_installation_config)
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
