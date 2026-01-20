import pytest
from kubetester import create_or_update_configmap, read_secret
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    create_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_x509_mongodb_tls_certs,
    get_mongodb_x509_subject,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import bootstrap_ca_issuer, get_central_cluster_client

CA_ISSUER_1_NAME = "ca-issuer-1"
CA_ISSUER_2_NAME = "ca-issuer-2"
MDB_RESOURCE = "my-replica-set-tls-test"

# This test emulates a setup where the server certificates are issued by a different CA than the client certificates (internal & agent).
# The server certificates also do not have "client auth" EKU, while the internal cluster certificates do not have "server auth" EKU.
# To use both CAs, we combine their root CA certificates into a single ConfigMap which is then referenced in the MDB spec.


@pytest.fixture(scope="module")
def diff_issuers(cert_manager: str, namespace: str):
    # Bootstrap two different CA issuers
    # This works by first creating two self-signed issuers, then issuing 2 CA certificates from them
    # which are then used to create the CA issuers.
    bootstrap_ca_issuer(
        name=CA_ISSUER_1_NAME,
        namespace=namespace,
        api_client=get_central_cluster_client(),
        self_signed_issuer_name="self-signed-issuer-1",
    )
    bootstrap_ca_issuer(
        name=CA_ISSUER_2_NAME,
        namespace=namespace,
        api_client=get_central_cluster_client(),
        self_signed_issuer_name="self-signed-issuer-2",
    )


@pytest.fixture(scope="module")
def server_certs(diff_issuers, namespace: str):
    spec = get_mongodb_x509_subject(namespace)

    # Remove client auth from server cert
    spec["usages"] = ["digital signature", "key encipherment", "server auth"]
    create_mongodb_tls_certs(
        CA_ISSUER_1_NAME, namespace, MDB_RESOURCE, bundle_secret_name=f"mdb-{MDB_RESOURCE}-cert", spec=spec
    )

    # Previously, internal certs also had server auth
    # Notice it is issued by a different CA
    spec["usages"] = ["digital signature", "key encipherment", "client auth"]
    create_mongodb_tls_certs(
        CA_ISSUER_2_NAME, namespace, MDB_RESOURCE, bundle_secret_name=f"mdb-{MDB_RESOURCE}-clusterfile", spec=spec
    )


@pytest.fixture(scope="module")
def agent_certs(diff_issuers, namespace: str):
    # Issued by different CA than server cert
    create_x509_agent_tls_certs(CA_ISSUER_2_NAME, namespace, MDB_RESOURCE, secret_prefix="mdb")


@pytest.fixture(scope="module")
def combined_issuers_configmap(diff_issuers, namespace: str) -> str:
    # Combine the CA certificates from both issuers into a single ConfigMap
    ca1 = read_secret(name=CA_ISSUER_1_NAME + "-ca-key-pair", namespace=namespace)["ca.crt"]
    ca2 = read_secret(name=CA_ISSUER_2_NAME + "-ca-key-pair", namespace=namespace)["ca.crt"]

    combined = ca1 + "\n" + ca2

    data = {"ca-pem": combined}

    name = "combined-issuers-ca"
    create_or_update_configmap(namespace, name, data)

    return name


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs, agent_certs, combined_issuers_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-x509-all-options-rs.yaml"), namespace=namespace, name=MDB_RESOURCE)
    res["spec"]["security"]["tls"]["ca"] = combined_issuers_configmap
    res["spec"]["security"]["certsSecretPrefix"] = "mdb"
    return res.update()


@skip_if_local
@pytest.mark.e2e_tls_different_ca_for_server_and_client
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_x509_configure_all_options_rs
class TestReplicaSetEnableAllOptions(KubernetesTester):
    def test_gets_to_running_state(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated(self):
        ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        ac_tester.assert_internal_cluster_authentication_enabled()
        ac_tester.assert_authentication_enabled()
