from pytest import mark
from kubetester import get_statefulset
from kubetester.kubetester import KubernetesTester
import subprocess

OPERATOR_NAME = "mongodb-enterprise-operator"


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
    result = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{vault_name}-0",
        namespace=vault_namespace,
        cmd=cmd,
        container="vault",
    )
    assert "Success!" in result


@mark.e2e_vault_setup
def test_enable_kubernetes_auth(vault_name: str, vault_namespace: str):
    # enable Kubernetes auth for Vault
    cmd = [
        "vault",
        "auth",
        "enable",
        "kubernetes",
    ]

    result = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{vault_name}-0",
        namespace=vault_namespace,
        cmd=cmd,
        container="vault",
    )

    assert "Success!" in result or "path is already in use at kubernetes" in result

    cmd = [
        "cat",
        "/var/run/secrets/kubernetes.io/serviceaccount/token",
    ]

    token = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{vault_name}-0",
        namespace=vault_namespace,
        cmd=cmd,
        container="vault",
    )

    cmd = [
        "vault",
        "write",
        "auth/kubernetes/config",
        f"token_reviewer_jwt={token}",
        "kubernetes_host=https://${KUBERNETES_PORT_443_TCP_ADDR}:443",
        "kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
    ]

    result = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{vault_name}-0",
        namespace=vault_namespace,
        cmd=cmd,
        container="vault",
    )

    assert "Success!" in result


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
    result = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{vault_name}-0",
        namespace=vault_namespace,
        cmd=cmd,
        container="vault",
    )

    assert "Success!" in result


@mark.e2e_vault_setup
def test_install_deploy():
    pass
