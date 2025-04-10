import time

from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import get_pod_when_ready, get_pod_when_running
from kubetester.certs import create_vault_certs
from kubetester.helm import helm_install_from_chart
from kubetester.kubetester import KubernetesTester
from pytest import fixture

from . import run_command_in_vault, vault_namespace_name, vault_sts_name


@fixture(scope="module")
def vault(namespace: str, version="v0.17.1", name="vault") -> str:

    try:
        KubernetesTester.create_namespace(name)
    except ApiException as e:
        pass
    helm_install_from_chart(
        namespace=name,
        release=name,
        chart=f"hashicorp/vault",
        version=version,
        custom_repo=("hashicorp", "https://helm.releases.hashicorp.com"),
    )

    # check if vault pod is running
    # We need to perform the initialization of the vault in order to the pod
    # to be ready. But we need to wait for it to be running in order to run commands in it.
    get_pod_when_running(
        namespace=name,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name={name}",
    )
    perform_vault_initialization(name, name)

    # check if vault pod is ready
    get_pod_when_ready(
        namespace=name,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name={name}",
    )

    # check if vault agent injector pod is ready
    get_pod_when_ready(
        namespace=name,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name=vault-agent-injector",
    )

    return name


@fixture(scope="module")
def vault_tls(namespace: str, issuer: str, vault_namespace: str, version="v0.17.1", name="vault") -> str:

    try:
        KubernetesTester.create_namespace(vault_namespace)
    except ApiException as e:
        pass
    create_vault_certs(namespace, issuer, vault_namespace, name, "vault-tls")

    helm_install_from_chart(
        namespace=name,
        release=name,
        chart=f"hashicorp/vault",
        version=version,
        custom_repo=("hashicorp", "https://helm.releases.hashicorp.com"),
        override_path="/tests/vaultconfig/override.yaml",
    )

    # check if vault pod is running
    get_pod_when_running(
        namespace=name,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name={name}",
    )
    perform_vault_initialization(name, name)

    # check if vault pod is ready
    get_pod_when_ready(
        namespace=name,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name={name}",
    )

    # check if vault agent injector pod is ready
    get_pod_when_ready(
        namespace=name,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name=vault-agent-injector",
    )


def perform_vault_initialization(namespace: str, name: str):

    #  If the pod is already ready, this means we don't have to perform anything
    pods = client.CoreV1Api().list_namespaced_pod(
        namespace,
        label_selector=f"app.kubernetes.io/instance={name},app.kubernetes.io/name={name}",
    )

    pod = pods.items[0]
    if pod.status.conditions is not None:
        for condition in pod.status.conditions:
            if condition.type == "Ready" and condition.status == "True":
                return

    run_command_in_vault(name, name, ["mkdir", "-p", "/vault/data"], [])

    response = run_command_in_vault(name, name, ["vault", "operator", "init"], ["Unseal"])

    response = response.split("\n")
    unseal_keys = []
    for i in range(5):
        unseal_keys.append(response[i].split(": ")[1])

    for i in range(3):
        run_command_in_vault(name, name, ["vault", "operator", "unseal", unseal_keys[i]], [])

    token = response[6].split(": ")[1]
    run_command_in_vault(name, name, ["vault", "login", token])

    run_command_in_vault(name, name, ["vault", "secrets", "enable", "-path=secret/", "kv-v2"])


@fixture(scope="module")
def vault_url(vault_name: str, vault_namespace: str) -> str:
    return f"{vault_name}.{vault_namespace}.svc.cluster.local"


@fixture(scope="module")
def vault_namespace() -> str:
    return vault_namespace_name()


@fixture(scope="module")
def vault_operator_policy_name() -> str:
    return "mongodbenterprise"


@fixture(scope="module")
def vault_name() -> str:
    return vault_sts_name()


@fixture(scope="module")
def vault_database_policy_name() -> str:
    return "mongodbenterprisedatabase"


@fixture(scope="module")
def vault_appdb_policy_name() -> str:
    return "mongodbenterpriseappdb"


@fixture(scope="module")
def vault_om_policy_name() -> str:
    return "mongodbenterpriseopsmanager"
