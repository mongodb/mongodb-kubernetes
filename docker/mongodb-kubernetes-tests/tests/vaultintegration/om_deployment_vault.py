import time
from typing import Optional

import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import (
    create_configmap,
    create_secret,
    delete_secret,
    get_statefulset,
    read_secret,
)
from kubetester.certs import create_mongodb_tls_certs, create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_pods
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark

from . import run_command_in_vault, store_secret_in_vault
from ..conftest import APPDB_SA_NAME, OM_SA_NAME, OPERATOR_NAME

OM_NAME = "om-basic"


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    prefix = "prefix"
    return create_ops_manager_tls_certs(
        issuer,
        namespace,
        om_name=OM_NAME,
        secret_name=f"{prefix}-{OM_NAME}-cert",
        secret_backend="Vault",
    )


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer,
        namespace,
        f"{OM_NAME}-db",
        f"appdb-{OM_NAME}-db-cert",
        secret_backend="Vault",
        vault_subpath="appdb",
    )
    return "appdb"


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    appdb_certs: str,
    issuer_ca_configmap: str,
    ops_manager_certs: str,
) -> MongoDBOpsManager:
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
    om["spec"]["security"] = {
        "tls": {"ca": issuer_ca_configmap},
        "certsSecretPrefix": "prefix",
    }
    om["spec"]["applicationDatabase"]["security"] = {
        "tls": {
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": "appdb",
    }
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    return om.create()


@mark.e2e_vault_setup_om
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
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

    response = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])

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
def test_put_admin_credentials_to_vault(namespace: str, vault_namespace: str, vault_name: str):
    admin_credentials_secret_name = "ops-manager-admin-secret"
    # read the -admin-secret from namespace and store in vault
    data = read_secret(namespace, admin_credentials_secret_name)
    path = f"secret/mongodbenterprise/operator/{namespace}/{admin_credentials_secret_name}"
    store_secret_in_vault(vault_namespace, vault_name, data, path)
    delete_secret(namespace, admin_credentials_secret_name)


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
        "mongodbenterpriseappdb",
        "/tmp/appdb-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_create_om_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/opsmanager-policy.hcl",
        "/tmp/om-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterpriseopsmanager",
        "/tmp/om-policy.hcl",
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
def test_enable_vault_role_for_om_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_om_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_om_policy_name}",
        f"bound_service_account_names={OM_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_om_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_om
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vault_setup_om
def test_no_admin_key_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{namespace}-{ops_manager.name}-admin-key")


@mark.e2e_vault_setup_om
def test_no_gen_key_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-gen-key")


@mark.e2e_vault_setup_om
def test_appdb_reached_running_and_pod_count(ops_manager: MongoDBOpsManager, namespace: str):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    # check AppDB has 4 containers(+1 because of vault-agent)
    for pod_name in get_pods(ops_manager.name + "-db-{}", 3):
        pod = client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
        assert len(pod.spec.containers) == 4


@mark.e2e_vault_setup_om
def test_no_appdb_connection_string_secret(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-connection-string")


@mark.e2e_vault_setup_om
def test_no_db_agent_password_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-agent-password")


@mark.e2e_vault_setup_om
def test_no_db_scram_password_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-om-user-scram-credentials")


@mark.e2e_vault_setup_om
def test_no_om_password_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-om-password")


@mark.e2e_vault_setup_om
def test_no_db_keyfile_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-keyfile")


@mark.e2e_vault_setup_om
def test_no_db_automation_config_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-config")


@mark.e2e_vault_setup_om
def test_no_db_monitoring_automation_config_secret_in_kubernetes(namespace: str, ops_manager: MongoDBOpsManager):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{ops_manager.name}-db-monitoring-config")


@mark.e2e_vault_setup_om
def test_rotate_appdb_certs(
    ops_manager: MongoDBOpsManager,
    vault_namespace: str,
    vault_name: str,
    namespace: str,
):
    omTries = 10
    while omTries > 0:
        ops_manager.load()
        secret_name = f"appdb-{ops_manager.name}-db-cert"
        if secret_name not in ops_manager["metadata"]["annotations"]:
            omTries -= 1
            time.sleep(30)
            continue
        old_version = ops_manager["metadata"]["annotations"][secret_name]
        break

    cmd = [
        "vault",
        "kv",
        "patch",
        f"secret/mongodbenterprise/appdb/{namespace}/{secret_name}",
        "foo=bar",
    ]

    run_command_in_vault(vault_namespace, vault_name, cmd, ["version"])

    tries = 30
    while tries > 0:
        ops_manager.load()
        if old_version != ops_manager["metadata"]["annotations"][secret_name]:
            return
        tries -= 1
        time.sleep(30)
    pytest.fail("Not reached new annotation")
