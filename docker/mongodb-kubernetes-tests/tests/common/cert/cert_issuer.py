from typing import List, Optional

from kubetester.certs import create_mongodb_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import is_multi_cluster
from tests.common.multicluster.multicluster_utils import multi_cluster_service_names
from tests.conftest import get_central_cluster_client, get_member_cluster_clients


def create_appdb_certs(
    namespace: str,
    issuer: str,
    appdb_name: str,
    cluster_index_with_members: list[tuple[int, int]] = None,
    cert_prefix="appdb",
    clusterwide: bool = False,
    additional_domains: Optional[List[str]] = None,
) -> str:
    if cluster_index_with_members is None:
        cluster_index_with_members = [(0, 1), (1, 2)]

    appdb_cert_name = f"{cert_prefix}-{appdb_name}-cert"

    if is_multi_cluster():
        service_fqdns = [
            f"{svc}.{namespace}.svc.cluster.local"
            for svc in multi_cluster_service_names(appdb_name, cluster_index_with_members)
        ]
        create_multi_cluster_mongodb_tls_certs(
            issuer,
            appdb_cert_name,
            get_member_cluster_clients(),
            get_central_cluster_client(),
            service_fqdns=service_fqdns,
            namespace=namespace,
            clusterwide=clusterwide,
            additional_domains=additional_domains,
        )
    else:
        create_mongodb_tls_certs(
            issuer,
            namespace,
            appdb_name,
            appdb_cert_name,
            clusterwide=clusterwide,
            additional_domains=additional_domains,
        )

    return cert_prefix
