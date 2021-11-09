from pytest import mark
from kubetester import get_statefulset
from kubetester.kubetester import KubernetesTester
import subprocess


@mark.e2e_vault_setup
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
    vault

    # assert if vault statefulset is ready, this is sort of redundant(we already assert for pod phase)
    # but this is basic assertion at the moment, will remove in followup PR
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1


@mark.e2e_vault_setup
def test_create_vault_operator_policy_and_role(vault_name: str, vault_namespace: str):
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
def test_install_deploy():
    pass
