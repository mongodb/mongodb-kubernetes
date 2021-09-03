import pytest

from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.kubetester import fixture as load_fixture
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_sharded_cluster_certs,
)

from typing import Dict, List

MDB_RESOURCE_NAME = "test-x509-sc"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    create_sharded_cluster_certs(
        namespace,
        MDB_RESOURCE_NAME,
        shards=1,
        mongos_per_shard=3,
        config_servers=3,
        mongos=2,
    )


@pytest.fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@pytest.fixture(scope="module")
def sharded_cluster(
    namespace: str, server_certs: str, agent_certs: str, issuer_ca_configmap: str
) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-x509-sc.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_x509_sc
class TestClusterWithTLSCreation(KubernetesTester):
    def test_sharded_cluster_running(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)
