def get_annotations_with_placeholders_for_single_cluster():
    return {
        "podIndex": "value={podIndex}",
        "namespace": "value={namespace}",
        "resourceName": "value={resourceName}",
        "podName": "value={podName}",
        "statefulSetName": "value={statefulSetName}",
        "externalServiceName": "value={externalServiceName}",
        "mongodProcessDomain": "value={mongodProcessDomain}",
        "mongodProcessFQDN": "value={mongodProcessFQDN}",
    }


def get_annotations_with_placeholders_for_multi_cluster(prefix: str = ""):
    return {
        "podIndex": prefix + "value={podIndex}",
        "namespace": prefix + "value={namespace}",
        "resourceName": prefix + "value={resourceName}",
        "podName": prefix + "value={podName}",
        "statefulSetName": prefix + "value={statefulSetName}",
        "externalServiceName": prefix + "value={externalServiceName}",
        "mongodProcessDomain": prefix + "value={mongodProcessDomain}",
        "mongodProcessFQDN": prefix + "value={mongodProcessFQDN}",
        "clusterName": prefix + "value={clusterName}",
        "clusterIndex": prefix + "value={clusterIndex}",
    }


def get_expected_annotations_single_cluster(name: str, namespace: str, pod_idx: int):
    """Returns annotations with resolved placeholder in the context of
    running single-cluster deployment without using external domains, so
    with FQDNs from headless services"""
    return {
        "podIndex": f"value={pod_idx}",
        "namespace": f"value={namespace}",
        "resourceName": f"value={name}",
        "podName": f"value={name}-{pod_idx}",
        "statefulSetName": f"value={name}",
        "externalServiceName": f"value={name}-{pod_idx}-svc-external",
        "mongodProcessDomain": f"value={name}-svc.{namespace}.svc.cluster.local",
        "mongodProcessFQDN": f"value={name}-{pod_idx}.{name}-svc.{namespace}.svc.cluster.local",  # headless pod fqdn
    }


def get_expected_annotations_single_cluster_with_external_domain(
    name: str, namespace: str, pod_idx: int, external_domain: str
):
    """Returns annotations with resolved placeholder in the context of running
    single-cluster deployment with external domains, so with FQDNs from external domains"""
    pod_name = f"{name}-{pod_idx}"
    return {
        "podIndex": f"value={pod_idx}",
        "namespace": f"value={namespace}",
        "resourceName": f"value={name}",
        "podName": f"value={pod_name}",
        "statefulSetName": f"value={name}",
        "externalServiceName": f"value={pod_name}-svc-external",
        "mongodProcessDomain": f"value={external_domain}",
        "mongodProcessFQDN": f"value={pod_name}.{external_domain}",
    }


def get_expected_annotations_multi_cluster(
    name: str, namespace: str, pod_idx: int, cluster_name: str, cluster_index: int, prefix: str = ""
):
    """Returns annotations with resolved placeholders in the context of running
    multi-cluster deployment without external domains, so with FQDNs from pod services.
    """
    statefulset_name = f"{name}-{cluster_index}"
    pod_name = f"{statefulset_name}-{pod_idx}"

    return {
        "podIndex": f"{prefix}value={pod_idx}",
        "namespace": f"{prefix}value={namespace}",
        "resourceName": f"{prefix}value={name}",
        "podName": f"{prefix}value={pod_name}",
        "statefulSetName": f"{prefix}value={statefulset_name}",
        "externalServiceName": f"{prefix}value={pod_name}-svc-external",
        "mongodProcessDomain": f"{prefix}value={namespace}.svc.cluster.local",
        "mongodProcessFQDN": f"{prefix}value={pod_name}-svc.{namespace}.svc.cluster.local",
        "clusterName": f"{prefix}value={cluster_name}",
        "clusterIndex": f"{prefix}value={cluster_index}",
    }


def get_expected_annotations_multi_cluster_no_mesh(
    name: str,
    namespace: str,
    pod_idx: int,
    external_domain: str,
    cluster_name: str,
    cluster_index: int,
    prefix: str = "",
):
    """Returns annotations with resolved placeholder in the context of running
    multi-cluster deployment without a service mesh so with FQDNs from external domains.
    """
    statefulset_name = f"{name}-{cluster_index}"
    pod_name = f"{statefulset_name}-{pod_idx}"
    return {
        "podIndex": f"{prefix}value={pod_idx}",
        "namespace": f"{prefix}value={namespace}",
        "resourceName": f"{prefix}value={name}",
        "podName": f"{prefix}value={pod_name}",
        "statefulSetName": f"{prefix}value={statefulset_name}",
        "externalServiceName": f"{prefix}value={pod_name}-svc-external",
        "mongodProcessDomain": f"{prefix}value={external_domain}",
        "mongodProcessFQDN": f"{prefix}value={pod_name}.{external_domain}",
        "clusterName": f"{prefix}value={cluster_name}",
        "clusterIndex": f"{prefix}value={cluster_index}",
    }
