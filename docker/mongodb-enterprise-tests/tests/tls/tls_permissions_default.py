from kubetester import find_fixture, try_load
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark


@fixture(scope="module")
def certs_secret_prefix(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, "test-tls-base-rs", "certs-test-tls-base-rs-cert")
    return "certs"


@fixture(scope="module")
def replica_set(
    issuer_ca_configmap: str,
    namespace: str,
    certs_secret_prefix,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("test-tls-base-rs.yaml"), namespace=namespace)
    resource.configure_custom_tls(issuer_ca_configmap, certs_secret_prefix)
    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@mark.e2e_replica_set_tls_default
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_replica_set_tls_default
def test_replica_set(replica_set: MongoDB):

    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_tls_default
def test_file_has_correct_permissions(namespace: str):
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
                res == "640"
            )  # stat has no option for decimal values, so we check for 640, which is the octal representation for 416
