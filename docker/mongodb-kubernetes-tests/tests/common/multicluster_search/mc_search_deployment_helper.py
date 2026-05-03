"""Two-cluster MongoDBMulti + MongoDBSearch fixture lifecycle helper.

Used by all MC search e2e tests to deploy the source RS across member
clusters, replicate Secrets, and surface per-cluster naming
(proxy-svc FQDN per cluster index, etc.) consistently.

The cluster-index ordering is the registration order in `member_cluster_clients`
— that mirrors the operator's `clusterSpecList[].clusterName` ordering, which
is what B3's stable cluster-index annotation pins.
"""
from typing import Mapping

from kubernetes.client import CoreV1Api
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class MCSearchDeploymentHelper:
    """Encapsulates 2-cluster MongoDBMulti+MongoDBSearch fixture knobs.

    Provides:
        - namespace
        - cluster index lookup by clusterName (matches operator's annotation)
        - per-cluster proxy-svc FQDN string for `mongotHost`
    """

    def __init__(
        self,
        namespace: str,
        mdb_resource_name: str,
        mdbs_resource_name: str,
        member_cluster_clients: Mapping[str, CoreV1Api],
    ) -> None:
        self.namespace = namespace
        self.mdb_resource_name = mdb_resource_name
        self.mdbs_resource_name = mdbs_resource_name
        self._member_cluster_clients = dict(member_cluster_clients)
        self._cluster_indices = {
            name: idx for idx, name in enumerate(self._member_cluster_clients)
        }

    def member_cluster_names(self) -> list[str]:
        return list(self._member_cluster_clients.keys())

    def cluster_index(self, cluster_name: str) -> int:
        if cluster_name not in self._cluster_indices:
            raise KeyError(f"unknown member cluster: {cluster_name!r}")
        return self._cluster_indices[cluster_name]

    def member_clients(self) -> Mapping[str, CoreV1Api]:
        return self._member_cluster_clients

    def proxy_svc_fqdn(self, cluster_name: str) -> str:
        """Return the cluster-index-suffixed proxy Service FQDN.

        Pattern: `{mdbs}-search-{clusterIndex}-proxy-svc.{ns}.svc.cluster.local`.

        This is the value that `mongotHost` should be set to on the
        per-cluster mongod (via `MongoDBMulti.spec.clusterSpecList[i]
        .additionalMongodConfig.setParameter.mongotHost`). It does NOT
        include the port; callers append `:<port>` as needed.
        """
        idx = self.cluster_index(cluster_name)
        return (
            f"{self.mdbs_resource_name}-search-{idx}-proxy-svc"
            f".{self.namespace}.svc.cluster.local"
        )
