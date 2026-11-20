from typing import Mapping, Optional


def cluster_proxy_url(kube_config: Mapping, context: Optional[str] = None) -> Optional[str]:
    """The proxy-url of the cluster of a kubeconfig context (the current one by default).

    kubectl sends its requests through that proxy, the kubernetes client ignores the key."""
    context = context or kube_config.get("current-context")
    cluster = next((c["context"]["cluster"] for c in kube_config.get("contexts", []) if c["name"] == context), None)
    return next((c["cluster"].get("proxy-url") for c in kube_config.get("clusters", []) if c["name"] == cluster), None)
