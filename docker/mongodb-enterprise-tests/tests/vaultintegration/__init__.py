from typing import Optional, List
from kubetester.kubetester import KubernetesTester


def run_command_in_vault(
    vault_namespace: str,
    vault_name: str,
    cmd: List[str],
    expected_message: Optional[List[str]] = None,
) -> str:
    result = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{vault_name}-0",
        namespace=vault_namespace,
        cmd=cmd,
        container="vault",
    )

    if expected_message is None:
        expected_message = ["Success!"]

    if not expected_message:
        return result

    for message in expected_message:
        if message in result:
            return result

    raise ValueError(f"Command failed. Got {result}")


def vault_sts_name() -> str:
    return "vault"


def vault_namespace_name() -> str:
    return "vault"
