from typing import Optional

from kubernetes import client
from kubetester import create_secret, delete_secret, get_statefulset, read_secret
from kubetester.certs import Certificate
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_pods, is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark

from . import run_command_in_vault, store_secret_in_vault
from ..constants import APPDB_SA_NAME, DATABASE_SA_NAME, OM_SA_NAME, OPERATOR_NAME

MDB_RESOURCE = "my-replica-set"
OM_NAME = "om-basic"


@fixture(scope="module")
def replica_set(
    namespace: str,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource.create()

    return resource


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
    om["spec"]["backup"] = {
        "enabled": False,
    }
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    return om.create()


@mark.e2e_vault_setup_tls
def test_vault_creation(vault_tls: str, vault_name: str, vault_namespace: str, issuer: str):

    # assert if vault statefulset is ready, this is sort of redundant(we already assert for pod phase)
    # but this is basic assertion at the moment, will remove in followup PR
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1


@mark.e2e_vault_setup_tls
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


@mark.e2e_vault_setup_tls
def test_enable_kubernetes_auth(vault_name: str, vault_namespace: str):
    # enable Kubernetes auth for Vault
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


@mark.e2e_vault_setup_tls
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


@mark.e2e_vault_setup_tls
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


@mark.e2e_vault_setup_tls
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


@mark.e2e_vault_setup_tls
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


@mark.e2e_vault_setup_tls
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


@mark.e2e_vault_setup_tls
def test_put_admin_credentials_to_vault(namespace: str, vault_namespace: str, vault_name: str):
    admin_credentials_secret_name = "ops-manager-admin-secret"
    # read the -admin-secret from namespace and store in vault
    data = read_secret(namespace, admin_credentials_secret_name)
    path = f"secret/mongodbenterprise/operator/{namespace}/{admin_credentials_secret_name}"
    store_secret_in_vault(vault_namespace, vault_name, data, path)
    delete_secret(namespace, admin_credentials_secret_name)


@mark.e2e_vault_setup_tls
def test_remove_cert_and_key_from_secret(namespace: str):
    data = read_secret(namespace, "vault-tls")
    cert = Certificate(name="vault-tls", namespace=namespace).load()
    cert.delete()
    del data["tls.crt"]
    del data["tls.key"]
    delete_secret(namespace, "vault-tls")
    create_secret(namespace, "vault-tls", data)


@mark.e2e_vault_setup_tls
def test_operator_install_with_vault_backend(
    operator_vault_secret_backend_tls: Operator,
):
    operator_vault_secret_backend_tls.assert_is_running()


@mark.e2e_vault_setup_tls
def test_store_om_credentials_in_vault(vault_namespace: str, vault_name: str, namespace: str):
    credentials = read_secret(namespace, "my-credentials")
    store_secret_in_vault(
        vault_namespace,
        vault_name,
        credentials,
        f"secret/mongodbenterprise/operator/{namespace}/my-credentials",
    )

    cmd = [
        "vault",
        "kv",
        "get",
        f"secret/mongodbenterprise/operator/{namespace}/my-credentials",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=["publicApiKey"])
    delete_secret(namespace, "my-credentials")


@mark.e2e_vault_setup_tls
def test_create_database_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/database-policy.hcl",
        "/tmp/database-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprisedatabase",
        "/tmp/database-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_tls
def test_enable_vault_role_for_database_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_database_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_database_policy_name}",
        f"bound_service_account_names={DATABASE_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_database_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup_tls
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vault_setup_tls
def test_mdb_created(replica_set: MongoDB, namespace: str):
    replica_set.assert_reaches_phase(Phase.Running, timeout=500, ignore_errors=True)
    for pod_name in get_pods(MDB_RESOURCE + "-{}", 3):
        pod = client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
        if is_default_architecture_static():
            assert len(pod.spec.containers) == 4
        else:
            assert len(pod.spec.containers) == 2


@mark.e2e_vault_setup_tls
def test_no_cert_in_secret(namespace: str):
    data = read_secret(namespace, "vault-tls")
    assert "tls.crt" not in data
    assert "tls.key" not in data
