from typing import Optional

from kubernetes.client import ApiClient, AppsV1Api, CoreV1Api
from kubernetes.client.rest import ApiException
from kubetester.kubetester import run_periodically
from kubetester.multicluster_client import MultiClusterClient


def create_internet_facing_nlb(
    api_client: Optional[ApiClient],
    namespace: str,
    *,
    name: str,
    selector_app: str,
    port: int,
    port_name: str,
    security_groups: list[str],
    external_dns_hostname: Optional[str] = None,
    cluster_id: str = "",
) -> None:
    """Create-or-update a corp-SG-locked internet-facing NLB Service selecting ``app=selector_app``.

    Fail-closed: refuses to provision an internet-facing NLB without a corp security group, so a
    misconfigured run can never default to an open (0.0.0.0/0) load balancer.
    """
    if not security_groups:
        raise ValueError(
            f"[{cluster_id}] refusing to create internet-facing NLB {name!r} without a corp security "
            "group; pass the provisioned prefix-locked SG(s)"
        )
    annotations = {
        "service.beta.kubernetes.io/aws-load-balancer-type": "external",
        "service.beta.kubernetes.io/aws-load-balancer-nlb-target-type": "instance",
        "service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing",
        "service.beta.kubernetes.io/aws-load-balancer-security-groups": ",".join(security_groups),
        # BYO frontend SG ⇒ the AWS LB Controller no longer auto-opens the node SG to the NLB;
        # without this the targets fail health checks (i/o timeout).
        "service.beta.kubernetes.io/aws-load-balancer-manage-backend-security-group-rules": "true",
    }
    if external_dns_hostname:
        annotations["external-dns.alpha.kubernetes.io/hostname"] = external_dns_hostname
    manifest = {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {"name": name, "labels": {"app": selector_app}, "annotations": annotations},
        "spec": {
            "type": "LoadBalancer",
            "selector": {"app": selector_app},
            "ports": [{"name": port_name, "port": port, "targetPort": port}],
        },
    }
    core = CoreV1Api(api_client=api_client)
    try:
        core.create_namespaced_service(namespace, manifest)
    except ApiException as exc:
        if exc.status != 409:
            raise
        core.patch_namespaced_service(name, namespace, manifest)


def wait_for_nlb_hostname(
    api_client: Optional[ApiClient], namespace: str, name: str, *, cluster_id: str = "", timeout: int = 300
) -> str:
    """Block until the NLB Service has an ingress hostname/IP and return it."""
    core = CoreV1Api(api_client=api_client)
    result: dict = {}

    def check():
        svc = core.read_namespaced_service(name, namespace)
        ingress = (svc.status.load_balancer.ingress or []) if svc.status.load_balancer else []
        if ingress and (ingress[0].hostname or ingress[0].ip):
            result["host"] = ingress[0].hostname or ingress[0].ip
            return True, f"NLB provisioned: {result['host']}"
        return False, "NLB hostname not yet assigned"

    run_periodically(check, timeout=timeout, sleep_time=10, msg=f"[{cluster_id}] NLB {name} provisioning")
    return result["host"]


def multi_cluster_pod_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster pod names for given replica set name and a list of member counts in member clusters."""
    result_list = []
    for cluster_index, members in cluster_index_with_members:
        result_list.extend([f"{replica_set_name}-{cluster_index}-{pod_idx}" for pod_idx in range(0, members)])

    return result_list


def multi_cluster_service_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster service names for given replica set name and a list of member counts in member clusters."""
    return [f"{pod_name}-svc" for pod_name in multi_cluster_pod_names(replica_set_name, cluster_index_with_members)]


def assert_deployment_ready_in_cluster(apps: AppsV1Api, *, name: str, namespace: str) -> None:
    """Assert Deployment `name`/`namespace` has all spec.replicas ready."""
    dep = apps.read_namespaced_deployment(name=name, namespace=namespace)
    ready = dep.status.ready_replicas or 0
    desired = dep.spec.replicas or 0
    if ready != desired or desired == 0:
        raise AssertionError(f"Deployment {namespace}/{name}: ready_replicas={ready}/{desired}")


def assert_workload_ready_in_cluster(
    mcc: MultiClusterClient,
    namespace: str,
    sts_ready: dict[str, int],
    deployment_name: str,
    timeout: int = 240,
) -> None:
    """Converge (poll) rather than snapshot: a single read right after a CR reports Running
    can observe stale member-cluster informer state.
    """
    assert sts_ready, (
        f"[{mcc.cluster_name}] assert_workload_ready_in_cluster called with empty sts_ready map — "
        f"the StatefulSet readiness check would be vacuously green"
    )
    apps = mcc.apps_v1_api()

    def check():
        try:
            for name, want in sts_ready.items():
                sts = mcc.read_namespaced_stateful_set(name, namespace)
                ready = sts.status.ready_replicas or 0
                if ready != want:
                    return False, f"{name} ready={ready}/{want}"
            try:
                assert_deployment_ready_in_cluster(apps, name=deployment_name, namespace=namespace)
            except AssertionError as exc:
                return False, str(exc)
        except ApiException as exc:
            return False, f"read error: {exc.status}"
        return True, "all ready"

    run_periodically(check, timeout=timeout, sleep_time=5, msg=f"[{mcc.cluster_name}] workloads ready")
