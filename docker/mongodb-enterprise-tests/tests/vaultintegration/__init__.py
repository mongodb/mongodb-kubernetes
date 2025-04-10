from typing import Dict, List, Optional

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


def store_secret_in_vault(vault_namespace: str, vault_name: str, secretData: Dict[str, str], path: str):
    cmd = ["vault", "kv", "put", path]

    for k, v in secretData.items():
        cmd.append(f"{k}={v}")

    run_command_in_vault(
        vault_namespace,
        vault_name,
        cmd,
        expected_message=["created_time"],
    )


def assert_secret_in_vault(vault_namespace: str, vault_name: str, path: str, expected_message: List[str]):
    cmd = [
        "vault",
        "kv",
        "get",
        path,
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd, expected_message)
