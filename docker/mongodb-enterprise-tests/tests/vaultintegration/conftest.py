from pytest import fixture
from kubetester.helm import helm_install_from_chart
from kubetester import get_pod_when_ready


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


def vault_url(namespace: str) -> str:
    return "vault.vault.svc.cluster.local"
