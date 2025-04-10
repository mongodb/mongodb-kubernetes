from kubetester import find_fixture
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.omtester import get_rs_cert_names
from kubetester.operator import Operator
from pytest import fixture, mark


@fixture(scope="module")
def certs_secret_prefix(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, "test-tls-base-rs", "certs-test-tls-base-rs-cert")
    return "certs"


@fixture(scope="module")
def replica_set(issuer_ca_configmap: str, namespace: str, certs_secret_prefix) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("test-tls-base-rs.yaml"), namespace=namespace)

    resource["spec"]["podSpec"] = {
        "podTemplate": {
            "spec": {
                "volumes": [
                    {
                        "name": "secret-certs",
                        "secret": {
                            "defaultMode": 420,  # This is the decimal value corresponding to 0644 permissions, different from the default 0640 (416)
                        },
                    }
                ]
            }
        }
    }
    resource.configure_custom_tls(issuer_ca_configmap, certs_secret_prefix)
    return resource.create()


@mark.e2e_replica_set_tls_override
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_replica_set_tls_override
def test_replica_set(replica_set: MongoDB, namespace: str):

    certs = get_rs_cert_names(replica_set["metadata"]["name"], namespace, with_agent_certs=False)

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_tls_override
def test_file_has_correct_permissions(replica_set: MongoDB, namespace: str):
    # We test that the permissions are as expected by executing the stat
    # command on all the pem files in the secrets/certs directory
    cmd = [
        "/bin/sh",
        "-c",
        'stat -c "%a" /mongodb-automation/tls/..data/*',
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"test-tls-base-rs-{i}",
            namespace,
            cmd,
        ).splitlines()
        for res in result:
            assert (
                res == "644"
            )  # stat has no option for decimal values, so we check for 644, which is the octal representation for 420
