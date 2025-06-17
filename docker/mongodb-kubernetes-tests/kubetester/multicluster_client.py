from __future__ import annotations

from typing import Optional

import kubernetes.client


class MultiClusterClient:
    def __init__(
        self,
        api_client: kubernetes.client.ApiClient,
        cluster_name: str,
        cluster_index: Optional[int] = None,
    ):
        self.api_client = api_client
        self.cluster_name = cluster_name
        self.cluster_index = cluster_index

    def apps_v1_api(self) -> kubernetes.client.AppsV1Api:
        return kubernetes.client.AppsV1Api(self.api_client)

    def core_v1_api(self) -> kubernetes.client.CoreV1Api:
        return kubernetes.client.CoreV1Api(self.api_client)

    def read_namespaced_stateful_set(self, name: str, namespace: str):
        return self.apps_v1_api().read_namespaced_stateful_set(name, namespace)

    def list_namespaced_stateful_sets(self, namespace: str):
        return self.apps_v1_api().list_namespaced_stateful_set(namespace)

    def list_namespaced_services(self, namespace: str):
        return self.core_v1_api().list_namespaced_service(namespace)

    def read_namespaced_service(self, name: str, namespace: str):
        return self.core_v1_api().read_namespaced_service(name, namespace)

    def list_namespaced_config_maps(self, namespace: str):
        return self.core_v1_api().list_namespaced_config_map(namespace)

    def read_namespaced_config_map(self, name: str, namespace: str):
        return self.core_v1_api().read_namespaced_config_map(name, namespace)

    def read_namespaced_persistent_volume_claim(self, name: str, namespace: str):
        return self.core_v1_api().read_namespaced_persistent_volume_claim(name, namespace)

    def assert_sts_members_count(self, sts_name: str, namespace: str, expected_shard_members_in_cluster: int):
        try:
            sts = self.read_namespaced_stateful_set(sts_name, namespace)
            assert sts.spec.replicas == expected_shard_members_in_cluster
        except kubernetes.client.ApiException as api_exception:
            assert (
                0 == expected_shard_members_in_cluster and api_exception.status == 404
            ), f"expected {expected_shard_members_in_cluster} members, but received {api_exception.status} exception while reading {namespace}:{sts_name}"
