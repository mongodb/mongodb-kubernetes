import tempfile

import kubernetes
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import Certificate, create_multi_cluster_x509_user_cert
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_deploy_mongodb_multi_with_tls_and_authentication(mongodb_multi: MongoDBMulti | MongoDB, namespace: str):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_ops_manager_state_was_updated_correctly(mongodb_multi: MongoDBMulti | MongoDB):
    ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac_tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
    ac_tester.assert_authentication_mechanism_enabled("MONGODB-X509")
    ac_tester.assert_internal_cluster_authentication_enabled()


def test_create_mongodb_x509_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_x509_user: MongoDBUser,
    namespace: str,
):
    mongodb_x509_user.assert_reaches_phase(Phase.Updated, timeout=100)


def test_x509_user_connectivity(
    mongodb_multi: MongoDBMulti | MongoDB,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer: str,
    namespace: str,
    ca_path: str,
):
    with tempfile.NamedTemporaryFile(delete=False, mode="w") as cert_file:
        create_multi_cluster_x509_user_cert(
            multi_cluster_issuer, namespace, central_cluster_client, path=cert_file.name
        )
        tester = mongodb_multi.tester()
        tester.assert_x509_authentication(cert_file_name=cert_file.name, tlsCAFile=ca_path)


# TODO Replace and use this method to check that certificate rotation after enabling TLS and authentication mechanisms
# keeps the resources reachable and in Running state.
def assert_certificate_rotation(central_cluster_client, mongodb_multi, namespace, certificate_name):
    cert = Certificate(name=certificate_name, namespace=namespace)
    cert.api = kubernetes.client.CustomObjectsApi(api_client=central_cluster_client)
    cert.load()
    cert["spec"]["dnsNames"].append("foo")  # Append DNS to cert to rotate the certificate
    cert.update()
    # FIXME the assertions below need to be replaced with a robust check that the agents are ready
    # and the TLS certificates are rotated.
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=100)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)
