#!/usr/bin/env python3

import pytest
from kubetester import read_configmap, read_secret, try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE = "test-tls-sc-managedcert"
SHARD_COUNT = 2


@pytest.fixture(scope="module")
def sc(namespace: str, issuer: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-sc-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource["spec"]["security"]["managedCertificate"] = {"enabled": True}
    try_load(resource)
    return resource


@pytest.mark.e2e_sharded_cluster_tls_managedcert
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_sharded_cluster_tls_managedcert
def test_sharded_cluster_running(sc: MongoDB):
    # Reaching Running is the functional proof that operator-managed certs work for every
    # component, each mongos/config/shard member presents a valid server cert, validates its
    # peers' clusterfile (client self-signed CA), and the agent authenticates - all with no
    # user-provided certs anywhere.
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_sharded_cluster_tls_managedcert
def test_operator_created_component_certs(sc: MongoDB, namespace: str):
    # Every component StatefulSet (mongos, config server, each shard) gets its own member
    # (server) cert and clusterfile (client) cert. The agent cert is one per resource.
    components = [f"{MDB_RESOURCE}-mongos", f"{MDB_RESOURCE}-config"]
    components += [f"{MDB_RESOURCE}-{i}" for i in range(SHARD_COUNT)]
    for component in components:
        for suffix in ("cert", "clusterfile"):
            secret_name = f"{component}-{suffix}"
            secret = read_secret(namespace, secret_name)
            assert "tls.crt" in secret, f"{secret_name} missing tls.crt"
            assert "tls.key" in secret, f"{secret_name} missing tls.key"

    agent_secret = read_secret(namespace, f"{MDB_RESOURCE}-agent-certs")
    assert "tls.crt" in agent_secret, f"{MDB_RESOURCE}-agent-certs missing tls.crt"


@pytest.mark.e2e_sharded_cluster_tls_managedcert
def test_single_shared_ca_bundle_for_the_whole_cluster(sc: MongoDB, namespace: str):
    # There is exactly one server CA bundle and one client CA bundle for the whole sharded
    # resource, named after the resource not per component. And every component's pods mount
    # them. This is the sharded-specific wiring, all components trust the same CA, just like a
    # single user-provided tls.ca serves every component in the non-managed path.
    for ca_secret_name in (f"{MDB_RESOURCE}-server-ca", f"{MDB_RESOURCE}-client-ca"):
        ca_secret = read_secret(namespace, ca_secret_name)
        assert "tls.crt" in ca_secret, f"{ca_secret_name} missing tls.crt"

    server_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-server-ca-bundle")
    assert server_ca_cm.get("ca-pem", "").strip() != "", "ca-pem (server CA) missing/empty"
    client_ca_cm = read_configmap(namespace, f"{MDB_RESOURCE}-managed-client-ca-bundle")
    assert client_ca_cm.get("clusterca-pem", "").strip() != "", "clusterca-pem (client CA) missing/empty"
    assert server_ca_cm["ca-pem"].strip() != client_ca_cm["clusterca-pem"].strip(), (
        "server and client self-signed CAs should be distinct roots"
    )


@pytest.mark.e2e_sharded_cluster_tls_managedcert
def test_scale_up_shards(sc: MongoDB):
    sc["spec"]["mongodsPerShardCount"] = 4
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1800)


@pytest.mark.e2e_sharded_cluster_tls_managedcert
def test_scale_down_shards(sc: MongoDB):
    sc["spec"]["mongodsPerShardCount"] = 3
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1800)
