def multi_cluster_pod_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster pod names for given replica set name and a list of member counts in member clusters."""
    result_list = []
    for cluster_index, members in cluster_index_with_members:
        result_list.extend([f"{replica_set_name}-{cluster_index}-{pod_idx}" for pod_idx in range(0, members)])

    return result_list


def multi_cluster_service_names(replica_set_name: str, cluster_index_with_members: list[tuple[int, int]]) -> list[str]:
    """List of multi-cluster service names for given replica set name and a list of member counts in member clusters."""
    return [f"{pod_name}-svc" for pod_name in multi_cluster_pod_names(replica_set_name, cluster_index_with_members)]
