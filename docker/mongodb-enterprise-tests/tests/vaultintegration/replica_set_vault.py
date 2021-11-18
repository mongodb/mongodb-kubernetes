from pytest import mark, fixture
from kubetester import get_statefulset, read_secret
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from kubetester.operator import Operator
from kubetester.certs import create_mongodb_tls_certs
from . import run_command_in_vault, store_secret_in_vault, assert_secret_in_vault
from kubetester.mongodb import MongoDB, Phase

OPERATOR_NAME = "mongodb-enterprise-operator"
MDB_RESOURCE = "my-replica-set"


@fixture(scope="module")
def replica_set(
    namespace: str,
    custom_mdb_version: str,
    server_certs: str,
    issuer_ca_configmap: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set.yaml"), MDB_RESOURCE, namespace
    )
    resource.set_version(custom_mdb_version)
    resource.create()

    return resource


@fixture(scope="module")
def server_certs(namespace: str, issuer: str) -> str:
    return create_mongodb_tls_certs(
        issuer,
        namespace,
        MDB_RESOURCE,
        f"{MDB_RESOURCE}-cert",
        secret_backend="Vault",
        vault_subpath="database",
    )


@mark.e2e_vault_setup
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
    vault

    # assert if vault statefulset is ready, this is sort of redundant(we already assert for pod phase)
    # but this is basic assertion at the moment, will remove in followup PR
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1


@mark.e2e_vault_setup
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


@mark.e2e_vault_setup
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


@mark.e2e_vault_setup
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


@mark.e2e_vault_setup
def test_operator_install_with_vault_backend(operator_vault_secret_backend: Operator):
    operator_vault_secret_backend.assert_is_running()


@mark.e2e_vault_setup
def test_secret_data_put_by_operator(
    vault_name: str,
    vault_namespace: str,
):
    cmd = [
        "vault",
        "kv",
        "get",
        "-field=keyfoo",
        "secret/mongodbenterprise/operator/keyfoo",
    ]
    result = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])
    assert result == "valuebar"


@mark.e2e_vault_setup
def test_store_om_credentials_in_vault(
    vault_namespace: str, vault_name: str, namespace: str
):
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
    run_command_in_vault(
        vault_namespace, vault_name, cmd, expected_message=["publicApiKey"]
    )


@mark.e2e_vault_setup
def test_mdb_created(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_vault_setup
def test_tls_certs_are_stored_in_vault(vault_namespace: str, vault_name: str):
    assert_secret_in_vault(
        vault_namespace,
        vault_name,
        f"secret/mongodbenterprise/database/{MDB_RESOURCE}-cert",
        ["tls.crt"],
    )
