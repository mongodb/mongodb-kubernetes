from kubernetes.client import AppsV1Api
from kubernetes.client.rest import ApiException
from kubetester.kubetester import run_periodically
from kubetester.multicluster_client import MultiClusterClient


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
