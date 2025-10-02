import pytest
from kubetester import try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    resource_name = "test-tls-base-rs"
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, resource_name, "test-tls-base-rs-cert")


@pytest.fixture(scope="module")
def replica_set(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-base-rs.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap

    # Set this ReplicaSet to allowSSL mode
    # this is the only mode that can go to "disabled" state.
    res["spec"]["additionalMongodConfig"] = {"net": {"ssl": {"mode": "allowSSL"}}}
    if try_load(res):
        return res
    return res.create()


@pytest.mark.e2e_disable_tls_scale_up
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_disable_tls_scale_up
def test_rs_is_running(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_disable_tls_scale_up
def test_tls_is_disabled_and_scaled_up(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["members"] = 5
    replica_set["spec"]["security"]["tls"]["enabled"] = False
    del replica_set["spec"]["additionalMongodConfig"]

    replica_set.update()

    # timeout is longer because the operator first needs to
    # disable TLS and then, scale up one by one.
    replica_set.assert_reaches_phase(Phase.Running, timeout=800)
