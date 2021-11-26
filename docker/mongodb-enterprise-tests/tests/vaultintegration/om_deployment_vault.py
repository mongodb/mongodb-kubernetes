from kubetester.opsmanager import MongoDBOpsManager
from tests.opsmanager.conftest import custom_appdb_version
from typing import Optional
from pytest import fixture, mark
import pytest
from kubetester.operator import Operator
from . import run_command_in_vault, store_secret_in_vault, assert_secret_in_vault
from kubetester import (
    get_statefulset,
    create_secret,
    delete_secret,
    create_configmap,
    read_secret,
)
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubernetes.client.rest import ApiException

OPERATOR_NAME = "mongodb-enterprise-operator"
APPDB_SA_NAME = "mongodb-enterprise-appdb"


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    return om.create()


@mark.e2e_vault_setup_om
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
    vault
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1


@mark.e2e_vault_setup_om
def test_create_vault_operator_policy(vault_name: str, vault_namespace: str):
    # copy hcl file from local machine to pod
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/operator-policy.hcl",
        "/tmp/operator-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprise",
        "/tmp/operator-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_enable_kubernetes_auth(vault_name: str, vault_namespace: str):
    cmd = [
        "vault",
        "auth",
        "enable",
        "kubernetes",
    ]

    run_command_in_vault(
        vault_namespace,
        vault_name,
        cmd,
        expected_message=["Success!", "path is already in use at kubernetes"],
    )

    cmd = [
        "cat",
        "/var/run/secrets/kubernetes.io/serviceaccount/token",
    ]

    token = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])
    cmd = ["env"]

    response = run_command_in_vault(
        vault_namespace, vault_name, cmd, expected_message=[]
    )

    response = response.split("\n")
    for line in response:
        l = line.strip()
        if str.startswith(l, "KUBERNETES_PORT_443_TCP_ADDR"):
            cluster_ip = l.split("=")[1]
            break
    cmd = [
        "vault",
        "write",
        "auth/kubernetes/config",
        f"token_reviewer_jwt={token}",
        f"kubernetes_host=https://{cluster_ip}:443",
        "kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
        "disable_iss_validation=true",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_enable_vault_role_for_operator_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_operator_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        "auth/kubernetes/role/mongodbenterprise",
        f"bound_service_account_names={OPERATOR_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_operator_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_operator_install_with_vault_backend(operator_vault_secret_backend: Operator):
    operator_vault_secret_backend.assert_is_running()


@mark.e2e_vault_setup_om
def test_create_appdb_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/appdb-policy.hcl",
        "/tmp/appdb-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprisedatabase",
        "/tmp/appdb-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_enable_vault_role_for_appdb_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_appdb_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_appdb_policy_name}",
        f"bound_service_account_names={APPDB_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_appdb_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vault_setup_om
def test_no_admin_key_secret_in_kubernetes(
    namespace: str, ops_manager: MongoDBOpsManager
):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{namespace}-{ops_manager.name}-admin-key")
