from __future__ import annotations
from typing import Dict, List, Optional

import kubernetes.client
from kubernetes import client
from kubetester import MongoDB
from kubetester.mongotester import MultiReplicaSetTester, MongoTester


class MultiClusterClient:
    def __init__(
        self,
        api_client: kubernetes.client.ApiClient,
        cluster_name: str,
        cluster_index: int,
    ):
        self.api_client = api_client
        self.cluster_name = cluster_name
        self.cluster_index = cluster_index


class MongoDBMulti(MongoDB):
    def __init__(self, *args, **kwargs):
        with_defaults = {
            "plural": "mongodbmulti",
            "kind": "MongoDBMulti",
            "group": "mongodb.com",
            "version": "v1",
        }
        with_defaults.update(kwargs)
        super(MongoDBMulti, self).__init__(*args, **with_defaults)

    def read_statefulsets(
        self, clients: List[MultiClusterClient]
    ) -> Dict[str, client.V1StatefulSet]:
        statefulsets = {}
        for mcc in clients:
            statefulsets[mcc.cluster_name] = client.AppsV1Api(
                api_client=mcc.api_client
            ).read_namespaced_stateful_set(
                f"{self.name}-{mcc.cluster_index}", self.namespace
            )
        return statefulsets

    def _get_item_spec(self, cluster_name: str) -> Dict:
        for spec in sorted(
            self["spec"]["clusterSpecList"]["clusterSpecs"],
            key=lambda x: x["clusterName"],
        ):
            if spec["clusterName"] == cluster_name:
                return spec

        raise ValueError(f"Cluster with name {cluster_name} not found!")

    def read_services(
        self, clients: List[MultiClusterClient]
    ) -> Dict[str, client.V1Service]:
        services = {}
        for mcc in clients:
            spec = self._get_item_spec(mcc.cluster_name)
            for (i, item) in enumerate(spec):
                services[mcc.cluster_name] = client.CoreV1Api(
                    api_client=mcc.api_client
                ).read_namespaced_service(
                    f"{self.name}-{mcc.cluster_index}-{i}-svc", self.namespace
                )
        return services

    def read_configmaps(
        self, clients: List[MultiClusterClient]
    ) -> Dict[str, client.V1ConfigMap]:
        configmaps = {}
        for mcc in clients:
            configmaps[mcc.cluster_name] = client.CoreV1Api(
                api_client=mcc.api_client
            ).read_namespaced_config_map("hostname-override", self.namespace)
        return configmaps

    def service_names(self) -> List[str]:
        service_names = []
        cluster_specs = sorted(
            self["spec"]["clusterSpecList"]["clusterSpecs"],
            key=lambda x: x["clusterName"],
        )
        for (i, spec) in enumerate(cluster_specs):
            for j in range(spec["members"]):
                service_names.append(f"{self.name}-{i}-{j}-svc")
        return service_names

    def tester(
        self,
        ca_path: Optional[str] = None,
        srv: bool = False,
        use_ssl: Optional[bool] = None,
    ) -> MongoTester:
        return MultiReplicaSetTester(
            service_names=self.service_names(),
            namespace=self.namespace,
            # TODO: tls, ca_path
        )
