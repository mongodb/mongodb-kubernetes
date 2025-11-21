from typing import List

from kubetester import read_secret
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_PEM_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert-pem"


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB, namespace: str):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_enabled_tls_mongodb_multi(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
    server_certs: str,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.load()
    mongodb_multi["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1300)

    # assert the presence of the generated pem certificates in each member cluster
    for client in member_cluster_clients:
        read_secret(
            namespace=namespace,
            name=BUNDLE_PEM_SECRET_NAME,
            api_client=client.api_client,
        )
