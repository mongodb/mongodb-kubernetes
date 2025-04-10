import time
from typing import Dict

import pytest
from kubernetes import client
from kubetester import create_secret, read_secret, try_load
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import helm_template
from kubetester.kubetester import create_testing_namespace
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import running_locally
from kubetester.mongodb import MongoDB, Phase, generic_replicaset
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture

"""
This is the test that verifies the procedure of configuring Operator in cluster-wide scope.
See https://docs.mongodb.com/kubernetes-operator/stable/tutorial/plan-k8s-operator-install/#cluster-wide-scope

"""


@fixture(scope="module")
def ops_manager_namespace(evergreen_task_id: str) -> str:
    # Note, that it's safe to create the namespace with constant name as the test must be run in isolated environment
    # and no collisions may happen
    return create_testing_namespace(evergreen_task_id, "om-namespace")


@fixture(scope="module")
def mdb_namespace(evergreen_task_id: str) -> str:
    return create_testing_namespace(evergreen_task_id, "mdb-namespace")


@fixture(scope="module")
def unmanaged_namespace(evergreen_task_id: str) -> str:
    return create_testing_namespace(evergreen_task_id, "unmanaged-namespace")


@fixture(scope="module")
def ops_manager(ops_manager_namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=ops_manager_namespace)
    resource["spec"]["backup"]["enabled"] = True
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)

    return resource


@fixture(scope="module")
def mdb(ops_manager: MongoDBOpsManager, mdb_namespace: str, namespace: str, custom_mdb_prev_version: str) -> MongoDB:
    # we need to copy credentials secret - as the global api key secret exists in Operator namespace only
    data = read_secret(namespace, ops_manager.api_key_secret(namespace))
    # we are now copying the secret from operator to mdb_namespace and the api_key_secret should therefore check for mdb_namespace, later
    # mongodb.configure will reference this new secret
    create_secret(mdb_namespace, ops_manager.api_key_secret(mdb_namespace), data)

    resource = (
        MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=mdb_namespace,
            name="my-replica-set",
        )
        .configure(ops_manager, "development")
        .set_version(custom_mdb_prev_version)
    )

    try_load(resource)

    return resource


@fixture(scope="module")
def unmanaged_mdb(ops_manager: MongoDBOpsManager, unmanaged_namespace: str) -> MongoDB:
    rs = generic_replicaset(unmanaged_namespace, "5.0.0", "unmanaged-mdb", ops_manager).create()

    yield rs

    rs.delete()


@pytest.mark.e2e_operator_clusterwide
def test_install_clusterwide_operator(operator_clusterwide: Operator):
    operator_clusterwide.assert_is_running()


@pytest.mark.e2e_operator_multi_namespaces
def test_install_multi_namespace_operator(
    operator_installation_config: Dict[str, str],
    ops_manager_namespace: str,
    mdb_namespace: str,
    namespace: str,
):
    """
    Installs the operator in default namespace and watches over both OM and MDB
    namespaces.
    """

    helm_args = operator_installation_config.copy()
    helm_args["operator.watchNamespace"] = ops_manager_namespace + "," + mdb_namespace

    Operator(namespace=namespace, helm_args=helm_args).install().assert_is_running()


@pytest.mark.e2e_operator_clusterwide
def test_configure_ops_manager_namespace(ops_manager_namespace: str, operator_installation_config: Dict[str, str]):
    """create a new namespace and configures all necessary service accounts there"""
    yaml_file = helm_template(
        helm_args={
            "registry.imagePullSecrets": operator_installation_config["registry.imagePullSecrets"],
        },
        templates="templates/database-roles.yaml",
        helm_options=[f"--namespace {ops_manager_namespace}"],
    )
    create_or_replace_from_yaml(client.api_client.ApiClient(), yaml_file)


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_create_image_pull_secret_ops_manager_namespace(
    namespace: str,
    ops_manager_namespace: str,
    operator_installation_config: Dict[str, str],
):
    """We need to copy image pull secrets to om namespace"""
    secret_name = operator_installation_config["registry.imagePullSecrets"]
    data = read_secret(namespace, secret_name)
    create_secret(ops_manager_namespace, secret_name, data, type="kubernetes.io/dockerconfigjson")


@pytest.mark.e2e_operator_clusterwide
def test_configure_mdb_namespace(mdb_namespace: str, operator_installation_config: Dict[str, str]):
    yaml_file = helm_template(
        helm_args={
            "registry.imagePullSecrets": operator_installation_config["registry.imagePullSecrets"],
        },
        templates="templates/database-roles.yaml",
        helm_options=[f"--namespace {mdb_namespace}"],
    )
    create_or_replace_from_yaml(client.api_client.ApiClient(), yaml_file)


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_create_image_pull_secret_mdb_namespace(
    namespace: str, mdb_namespace: str, operator_installation_config: Dict[str, str]
):
    secret_name = operator_installation_config["registry.imagePullSecrets"]
    data = read_secret(namespace, secret_name)
    create_secret(mdb_namespace, secret_name, data, type="kubernetes.io/dockerconfigjson")


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_create_om_in_separate_namespace(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.create_admin_secret()
    ops_manager.backup_status().assert_reaches_phase(
        Phase.Pending, msg_regexp=".*configuration is required for backup", timeout=900
    )
    ops_manager.get_om_tester().assert_healthiness()


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_check_k8s_resources(ops_manager: MongoDBOpsManager, ops_manager_namespace: str, namespace: str):
    """Verifying that all the K8s resources were created in an ops manager namespace"""
    assert ops_manager.read_statefulset().metadata.namespace == ops_manager_namespace
    assert ops_manager.read_backup_statefulset().metadata.namespace == ops_manager_namespace
    # api key secret is created in the Operator namespace for better access control
    ops_manager.read_api_key_secret(namespace)
    assert ops_manager.read_gen_key_secret().metadata.namespace == ops_manager_namespace
    assert ops_manager.read_appdb_generated_password_secret().metadata.namespace == ops_manager_namespace


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_create_mdb_in_separate_namespace(mdb: MongoDB, mdb_namespace: str):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
    mdb.assert_connectivity()
    assert mdb.read_statefulset().metadata.namespace == mdb_namespace


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_upgrade_mdb(mdb: MongoDB, custom_mdb_version):
    mdb.set_version(custom_mdb_version)

    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1000)
    mdb.assert_connectivity()
    mdb.tester().assert_version(custom_mdb_version)


@pytest.mark.e2e_operator_clusterwide
@pytest.mark.e2e_operator_multi_namespaces
def test_delete_mdb(mdb: MongoDB):
    mdb.delete()

    time.sleep(10)
    with pytest.raises(client.rest.ApiException):
        mdb.read_statefulset()


@pytest.mark.e2e_operator_multi_namespaces
def test_resources_on_unmanaged_namespaces_stay_cold(unmanaged_mdb: MongoDB):
    """
    For an unmanaged resource, the status should not be updated!
    """
    for i in range(10):
        time.sleep(5)

        unmanaged_mdb.reload()
        assert "status" not in unmanaged_mdb
