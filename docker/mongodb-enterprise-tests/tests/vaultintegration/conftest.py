from pytest import fixture
from kubetester.helm import helm_install_from_chart
from kubetester import get_pod_when_ready
from . import vault_sts_name, vault_namespace_name


@fixture(scope="module")
def vault(namespace: str, version="v0.17.1", name="vault") -> str:

    helm_install_from_chart(
        namespace=name,
        release=name,
        chart=f"hashicorp/vault",
        version=version,
        custom_repo=("hashicorp", "https://helm.releases.hashicorp.com"),
        helm_args={"server.dev.enabled": "true"},
    )

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
