#!/usr/bin/env python3

import pytest
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator

MDB_RESOURCE = "test-tls-base-rs-require-ssl"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MDB_RESOURCE,
        f"prefix-{MDB_RESOURCE}-cert",
        replicas=3,
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-base-rs-require-ssl.yaml"), namespace=namespace)

    res["spec"]["security"]["tls"] = {"ca": issuer_ca_configmap}
    # Setting security.certsSecretPrefix implicitly enables TLS
    res["spec"]["security"]["certsSecretPrefix"] = "prefix"
    return res.create()


@pytest.mark.e2e_replica_set_tls_certs_secret_prefix
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_replica_set_tls_certs_secret_prefix
def test_replica_set_running(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_replica_set_tls_certs_secret_prefix
@skip_if_local()
def test_mdb_is_not_reachable_with_no_ssl(mdb: MongoDB):
    mdb.tester(use_ssl=False).assert_no_connection()


@pytest.mark.e2e_replica_set_tls_certs_secret_prefix
@skip_if_local()
def test_mdb_is_reachable_with_ssl(mdb: MongoDB, ca_path: str):
    mdb.tester(use_ssl=True, ca_path=ca_path).assert_connectivity()
