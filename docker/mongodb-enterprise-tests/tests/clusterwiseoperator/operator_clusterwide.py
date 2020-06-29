import pytest
import time
from kubernetes import client
from kubernetes.client.rest import ApiException
from pytest import fixture

from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import helm_template
from kubetester.kubetester import (
    fixture as yaml_fixture,
    KubernetesTester,
    create_testing_namespace,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager

"""
This is the test that verifies the procedure of configuring Operator in cluster-wide scope.
See https://docs.mongodb.com/kubernetes-operator/stable/tutorial/plan-k8s-operator-install/#cluster-wide-scope

"""


@fixture(scope="module")
def operator_clusterwide(
    namespace: str,
    operator_version: str,
    operator_registry_url: str,
    om_init_registry_url: str,
    appdb_init_registry_url: str,
    om_registry_url: str,
    appdb_registry_url: str,
    ops_manager_name: str,
    appdb_name: str,
    managed_security_context: bool,
    image_pull_secrets: str,
) -> Operator:
    return Operator(
        namespace=namespace,
        operator_version=operator_version,
        operator_registry_url=operator_registry_url,
        init_om_registry_url=om_init_registry_url,
        init_appdb_registry_url=appdb_init_registry_url,
        ops_manager_registry_url=om_registry_url,
        appdb_registry_url=appdb_registry_url,
        ops_manager_name=ops_manager_name,
        appdb_name=appdb_name,
        managed_security_context=managed_security_context,
        image_pull_secrets=image_pull_secrets,
        helm_args={"operator.watchNamespace": "*"},
    ).install()


@fixture(scope="module")
def ops_manager_namespace(evergreen_task_id: str) -> str:
    # Note, that it's safe to create the namespace with constant name as the test must be run in isolated environment
    # and no collisions may happen
    return create_testing_namespace(evergreen_task_id, "om-namespace")


@fixture(scope="module")
def mdb_namespace(evergreen_task_id: str) -> str:
    return create_testing_namespace(evergreen_task_id, "mdb-namespace")


@fixture(scope="module")
def ops_manager(ops_manager_namespace) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=ops_manager_namespace
    )
    resource["spec"]["backup"]["enabled"] = True

    return resource.create()


@fixture(scope="module")
def mdb(ops_manager: MongoDBOpsManager, mdb_namespace: str, namespace: str):
    # we need to copy credentials secret - as the global api key secret exists in Operator namespace only
    data = KubernetesTester.read_secret(namespace, ops_manager.api_key_secret())
    KubernetesTester.create_secret(mdb_namespace, ops_manager.api_key_secret(), data)

    return (
        MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=mdb_namespace,
            name="my-replica-set",
        )
        .configure(ops_manager, "development")
        .create()
    )


@pytest.mark.e2e_operator_clusterwide
def test_install_clusterwide_operator(operator_clusterwide: Operator):
    operator_clusterwide.assert_is_running()


@pytest.mark.e2e_operator_clusterwide
def test_configure_ops_manager_namespace(ops_manager_namespace: str):
    """ create a new namespace and configures all necessary service accounts there """
    yaml_file = helm_template(
        helm_args={"namespace": ops_manager_namespace},
        templates="templates/database-roles.yaml",
    )
    create_or_replace_from_yaml(client.api_client.ApiClient(), yaml_file)


@pytest.mark.e2e_operator_clusterwide
def test_configure_mdb_namespace(mdb_namespace: str):
    yaml_file = helm_template(
        helm_args={"namespace": mdb_namespace},
        templates="templates/database-roles.yaml",
    )
    create_or_replace_from_yaml(client.api_client.ApiClient(), yaml_file)


@pytest.mark.e2e_operator_clusterwide
def test_create_om_in_separate_namespace(ops_manager: MongoDBOpsManager):
    ops_manager.create_admin_secret()
    ops_manager.backup_status().assert_reaches_phase(
        Phase.Pending, msg_regexp=".*configuration is required for backup", timeout=900
    )
    ops_manager.get_om_tester().assert_healthiness()


@pytest.mark.e2e_operator_clusterwide
def test_check_k8s_resources(
    ops_manager: MongoDBOpsManager, ops_manager_namespace: str, namespace: str
):
    """ Verifying that all the K8s resources were created in a ops manager namespace """
    assert ops_manager.read_statefulset().metadata.namespace == ops_manager_namespace
    assert (
        ops_manager.read_backup_statefulset().metadata.namespace
        == ops_manager_namespace
    )
    # api key secret is created in the Operator namespace for better access control
    ops_manager.read_api_key_secret(namespace)
    assert ops_manager.read_gen_key_secret().metadata.namespace == ops_manager_namespace
    assert (
        ops_manager.read_appdb_generated_password_secret().metadata.namespace
        == ops_manager_namespace
    )


@pytest.mark.e2e_operator_clusterwide
def test_create_mdb_in_separate_namespace(mdb: MongoDB, mdb_namespace: str):
    mdb.assert_reaches_phase(Phase.Running, timeout=350)
    mdb.assert_connectivity()
    assert mdb.read_statefulset().metadata.namespace == mdb_namespace


@pytest.mark.e2e_operator_clusterwide
def test_upgrade_mdb(mdb: MongoDB):
    mdb["spec"]["version"] = "4.2.2"

    mdb.update()
    mdb.assert_abandons_phase(Phase.Running)
    mdb.assert_reaches_phase(Phase.Running)
    mdb.assert_connectivity()
    mdb.tester().assert_version("4.2.2")


@pytest.mark.e2e_operator_clusterwide
def test_delete_mdb(mdb: MongoDB):
    mdb.delete()

    time.sleep(10)
    with pytest.raises(client.rest.ApiException):
        mdb.read_statefulset()
