from typing import Dict

import kubernetes
from kubetester import create_or_update_secret, read_secret
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import helm_template
from kubetester.kubetester import create_testing_namespace
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_evergreen_task_id,
    get_member_cluster_clients,
    get_multi_cluster_operator_clustermode,
    get_multi_cluster_operator_installation_config,
    get_operator_clusterwide,
    get_operator_installation_config,
    is_multi_cluster,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


def _prepare_om_namespace(ops_manager_namespace: str, operator_installation_config: dict[str, str]):
    """create a new namespace and configures all necessary service accounts there"""
    install_database_roles(
        ops_manager_namespace,
        operator_installation_config,
        api_client=get_central_cluster_client(),
    )


def install_database_roles(
    namespace: str,
    operator_installation_config: dict[str, str],
    api_client: kubernetes.client.ApiClient,
):
    try:
        yaml_file = helm_template(
            helm_args={
                "registry.imagePullSecrets": operator_installation_config["registry.imagePullSecrets"],
            },
            templates="templates/database-roles.yaml",
            helm_options=[f"--namespace {namespace}"],
        )
        create_or_replace_from_yaml(api_client, yaml_file)
    except Exception as e:
        print(f"Caught exception while installing database roles: {e}")
        raise e


def create_om_admin_secret(ops_manager_namespace: str, api_client: kubernetes.client.ApiClient = None):
    data = dict(
        Username="test-user",
        Password="@Sihjifutestpass21nnH",
        FirstName="foo",
        LastName="bar",
    )
    create_or_update_secret(
        namespace=ops_manager_namespace,
        name="ops-manager-admin-secret",
        data=data,
        api_client=api_client,
    ),


def prepare_multi_cluster_namespace(namespace: str, new_namespace: str):
    operator_installation_config = get_multi_cluster_operator_installation_config(namespace)
    image_pull_secret_name = operator_installation_config["registry.imagePullSecrets"]
    image_pull_secret_data = read_secret(namespace, image_pull_secret_name, api_client=get_central_cluster_client())
    for member_cluster_client in get_member_cluster_clients():
        install_database_roles(
            new_namespace,
            operator_installation_config,
            member_cluster_client.api_client,
        )
        create_testing_namespace(
            get_evergreen_task_id(),
            new_namespace,
            member_cluster_client.api_client,
            True,
        )
        create_or_update_secret(
            new_namespace,
            image_pull_secret_name,
            image_pull_secret_data,
            type="kubernetes.io/dockerconfigjson",
            api_client=member_cluster_client.api_client,
        )


def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource


def prepare_namespace(namespace: str, new_namespace: str, operator_installation_config):
    if is_multi_cluster():
        prepare_multi_cluster_namespace(namespace, new_namespace)
    else:
        _prepare_om_namespace(new_namespace, operator_installation_config)
    create_om_admin_secret(new_namespace, api_client=get_central_cluster_client())


@fixture(scope="module")
def om1(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    operator_installation_config: Dict[str, str],
) -> MongoDBOpsManager:
    prepare_namespace(namespace, "om-1", operator_installation_config)
    om = ops_manager("om-1", custom_version, custom_appdb_version)
    om.update()
    return om


@fixture(scope="module")
def om2(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    operator_installation_config: Dict[str, str],
) -> MongoDBOpsManager:
    prepare_namespace(namespace, "om-2", operator_installation_config)
    om = ops_manager("om-2", custom_version, custom_appdb_version)
    om.update()
    return om


@fixture(scope="module")
def om_operator_clusterwide(namespace: str):
    if is_multi_cluster():
        return get_multi_cluster_operator_clustermode(namespace)
    else:
        return get_operator_clusterwide(namespace, get_operator_installation_config(namespace))


@mark.e2e_om_multiple
def test_install_operator(om_operator_clusterwide: Operator):
    om_operator_clusterwide.assert_is_running()


@mark.e2e_om_multiple
def test_create_namespaces(evergreen_task_id: str):
    create_testing_namespace(evergreen_task_id, "om-1")
    create_testing_namespace(evergreen_task_id, "om-2")


@mark.e2e_om_multiple
def test_multiple_om_create(om1: MongoDBOpsManager, om2: MongoDBOpsManager):
    om1.om_status().assert_reaches_phase(Phase.Running, timeout=1100)
    om2.om_status().assert_reaches_phase(Phase.Running, timeout=1100)


@mark.e2e_om_multiple
def test_image_pull_secret_om_created_1(namespace: str, operator_installation_config: Dict[str, str]):
    """check if imagePullSecrets was cloned in the OM namespace"""
    secret_name = operator_installation_config["registry.imagePullSecrets"]
    secretDataInOperatorNs = read_secret(namespace, secret_name)
    secretDataInOmNs = read_secret("om-1", secret_name)
    assert secretDataInOperatorNs == secretDataInOmNs


@mark.e2e_om_multiple
def test_image_pull_secret_om_created_2(namespace: str, operator_installation_config: Dict[str, str]):
    """check if imagePullSecrets was cloned in the OM namespace"""
    secret_name = operator_installation_config["registry.imagePullSecrets"]
    secretDataInOperatorNs = read_secret(namespace, secret_name)
    secretDataInOmNs = read_secret("om-2", secret_name)
    assert secretDataInOperatorNs == secretDataInOmNs
