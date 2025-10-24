import pytest
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

    return res.create()


@pytest.mark.e2e_disable_tls_scale_up
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_disable_tls_scale_up
def test_rs_is_running(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_disable_tls_scale_up
def test_validation_error_on_simultaneous_tls_disable_and_scale(replica_set: MongoDB):
    """Test that attempting to disable TLS and scale simultaneously fails validation."""
    replica_set.load()
    replica_set["spec"]["members"] = 5
    replica_set["spec"]["security"]["tls"]["enabled"] = False
    del replica_set["spec"]["additionalMongodConfig"]

    try:
        replica_set.update()
        # If update succeeds, the test should fail
        assert False, "Expected validation error but update succeeded"
    except Exception as e:
        # Verify the error message contains our validation error
        error_message = str(e)
        assert "Cannot disable TLS and change member count simultaneously" in error_message, \
            f"Expected validation error about simultaneous TLS disable and scaling, got: {error_message}"


@pytest.mark.e2e_disable_tls_scale_up
def test_scale_up_without_tls_change(replica_set: MongoDB):
    """Test that scaling up without TLS changes works."""
    replica_set.load()
    replica_set["spec"]["members"] = 5

    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@pytest.mark.e2e_disable_tls_scale_up
def test_disable_tls_without_scaling(replica_set: MongoDB):
    """Test that disabling TLS without scaling works."""
    replica_set.load()
    replica_set["spec"]["security"]["tls"]["enabled"] = False
    del replica_set["spec"]["additionalMongodConfig"]

    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=800)

@pytest.mark.e2e_disable_tls_scale_up
def test_scale_down_after_tls_change(replica_set: MongoDB):
    """Test that scaling down after disabling TLS works."""
    replica_set.load()
    replica_set["spec"]["members"] = 3

    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)
