from __future__ import annotations

from typing import Dict, List, Optional

import kubernetes.client
from kubernetes import client
from kubetester import MongoDB
from kubetester.mongotester import MongoTester, MultiReplicaSetTester


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

    def list_namespaced_stateful_set(self, namespace: str):
        return self.apps_v1_api().list_namespaced_stateful_set(namespace)

    def read_namespaced_service(self, name: str, namespace: str):
        return self.core_v1_api().read_namespaced_service(name, namespace)

    def read_namespaced_config_map(self, name: str, namespace: str):
        return self.core_v1_api().read_namespaced_config_map(name, namespace)

    def read_namespaced_persistent_volume_claim(self, name: str, namespace: str):
        return self.core_v1_api().read_namespaced_persistent_volume_claim(name, namespace)


class MongoDBMulti(MongoDB):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbmulticluster",
            "kind": "MongoDBMultiCluster",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBMulti, self).__init__(*args, **with_defaults)

    def read_statefulsets(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1StatefulSet]:
        statefulsets = {}
        for mcc in clients:
            statefulsets[mcc.cluster_name] = mcc.read_namespaced_stateful_set(
                f"{self.name}-{mcc.cluster_index}", self.namespace
            )
        return statefulsets

    def get_item_spec(self, cluster_name: str) -> Dict:
        for spec in sorted(
            self["spec"]["clusterSpecList"],
            key=lambda x: x["clusterName"],
        ):
            if spec["clusterName"] == cluster_name:
                return spec

        raise ValueError(f"Cluster with name {cluster_name} not found!")

    def read_services(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1Service]:
        services = {}
        for mcc in clients:
            spec = self.get_item_spec(mcc.cluster_name)
            for i, item in enumerate(spec):
                services[mcc.cluster_name] = mcc.read_namespaced_service(
                    f"{self.name}-{mcc.cluster_index}-{i}-svc", self.namespace
                )
        return services

    def read_headless_services(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1Service]:
        services = {}
        for mcc in clients:
            services[mcc.cluster_name] = mcc.read_namespaced_service(
                f"{self.name}-{mcc.cluster_index}-svc", self.namespace
            )
        return services

    def read_configmaps(self, clients: List[MultiClusterClient]) -> Dict[str, client.V1ConfigMap]:
        configmaps = {}
        for mcc in clients:
            configmaps[mcc.cluster_name] = mcc.read_namespaced_config_map(
                f"{self.name}-hostname-override", self.namespace
            )
        return configmaps

    def service_names(self) -> List[str]:
        # TODO: this function does not account for previous
        # clusters being removed, the indices do not line up
        # and as a result the incorrect service name will be returned.
        service_names = []
        cluster_specs = sorted(
            self["spec"]["clusterSpecList"],
            key=lambda x: x["clusterName"],
        )
        for i, spec in enumerate(cluster_specs):
            for j in range(spec["members"]):
                service_names.append(f"{self.name}-{i}-{j}-svc")
        return service_names

    def tester(
        self,
        ca_path: Optional[str] = None,
        srv: bool = False,
        use_ssl: Optional[bool] = None,
        service_names: Optional[List[str]] = None,
        port="27017",
        external: bool = False,
    ) -> MongoTester:
        if service_names is None:
            service_names = self.service_names()

        return MultiReplicaSetTester(
            service_names=service_names,
            namespace=self.namespace,
            port=port,
            external=external,
            # TODO: tls, ca_path
        )
