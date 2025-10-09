import jsonpatch
import pytest
from kubernetes import client
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase

MDB_RESOURCE_NAME = "test-tls-additional-domains"


@pytest.fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MDB_RESOURCE_NAME,
        f"{MDB_RESOURCE_NAME}-cert",
        additional_domains=[
            "test-tls-additional-domains-0.additional-cert-test.com",
            "test-tls-additional-domains-1.additional-cert-test.com",
            "test-tls-additional-domains-2.additional-cert-test.com",
        ],
    )


@pytest.fixture(scope="module")
def mdb(namespace: str, server_certs: str, issuer_ca_configmap: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("test-tls-additional-domains.yaml"), namespace=namespace)
    res["spec"]["security"]["tls"]["ca"] = issuer_ca_configmap
    return res.create()


@pytest.mark.e2e_tls_rs_additional_certs
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@pytest.mark.e2e_tls_rs_additional_certs
class TestReplicaSetWithAdditionalCertDomains(KubernetesTester):
    def test_replica_set_is_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=400)

    @skip_if_local
    def test_can_still_connect(self, mdb: MongoDB, ca_path: str):
        tester = mdb.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()


@pytest.mark.e2e_tls_rs_additional_certs
class TestReplicaSetRemoveAdditionalCertDomains(KubernetesTester):
    def test_remove_additional_certs(self, namespace: str, mdb: MongoDB):
        # We don't have a nice way to delete fields from a resource specification
        # in our test env, so we need to achieve it with specific uses of patches
        body = {
            "op": "remove",
            "path": "/spec/security/tls/additionalCertificateDomains",
        }
        patch = jsonpatch.JsonPatch([body])

        last_transition = mdb.get_status_last_transition_time()

        mdb_resource = client.CustomObjectsApi().get_namespaced_custom_object(
            mdb.group,
            mdb.version,
            namespace,
            mdb.plural,
            mdb.name,
        )
        client.CustomObjectsApi().replace_namespaced_custom_object(
            mdb.group,
            mdb.version,
            namespace,
            mdb.plural,
            mdb.name,
            jsonpatch.apply_patch(mdb_resource, patch),
        )

        mdb.assert_state_transition_happens(last_transition)
        mdb.assert_reaches_phase(Phase.Running, timeout=400)

    @skip_if_local
    def test_can_still_connect(self, mdb: MongoDB, ca_path: str):
        tester = mdb.tester(use_ssl=True, ca_path=ca_path)
        tester.assert_connectivity()
