import pytest

from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.kubetester import fixture as load_fixture
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_sharded_cluster_certs,
    Certificate,
)

from typing import Dict, List

MDB_RESOURCE_NAME = "test-tls-base-sc-require-ssl"


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
def sharded_cluster(
    namespace: str, server_certs: str, issuer_ca_configmap: str
) -> MongoDB:
    res = MongoDB.from_yaml(
        load_fixture("test-tls-base-sc-require-ssl-custom-ca.yaml"), namespace=namespace
    )
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreation(KubernetesTester):
    def test_sharded_cluster_running(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self, ca_path: str):
        tester = ShardedClusterTester(
            MDB_RESOURCE_NAME, ssl=True, ca_path=ca_path, mongos_count=2
        )
        tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self):
        tester = ShardedClusterTester(MDB_RESOURCE_NAME, mongos_count=2)
        tester.assert_no_connection()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreationRunning(KubernetesTester):
    def test_mdb_should_reach_goal_state_after_scaling(self, sharded_cluster: MongoDB):
        sharded_cluster.load()
        sharded_cluster["spec"]["mongodsPerShardCount"] = 3
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self, ca_path: str):
        tester = ShardedClusterTester(
            MDB_RESOURCE_NAME, ssl=True, ca_path=ca_path, mongos_count=2
        )
        tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self):
        tester = ShardedClusterTester(MDB_RESOURCE_NAME, ssl=False, mongos_count=2)
        tester.assert_no_connection()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestCertificateIsRenewed(KubernetesTester):
    def test_mdb_reconciles_succesfully(self, sharded_cluster: MongoDB, namespace: str):
        cert = Certificate(
            name=f"{MDB_RESOURCE_NAME}-0-cert", namespace=namespace
        ).load()
        cert["spec"]["dnsNames"].append("foo")
        cert.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self, ca_path: str):
        tester = ShardedClusterTester(
            MDB_RESOURCE_NAME, ssl=True, ca_path=ca_path, mongos_count=2
        )
        tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self):
        tester = ShardedClusterTester(MDB_RESOURCE_NAME, mongos_count=2)
        tester.assert_no_connection()
