from typing import List

from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
    server_certs: str,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2400)
