import random

from pytest import mark, fixture

from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import find_fixture

from kubetester.mongodb import MongoDB, Phase
from kubetester.omtester import get_rs_cert_names
from kubetester.kubetester import KubernetesTester

from tests.opsmanager.om_ops_manager_https import create_tls_certs
from datetime import datetime, timezone
import time


@fixture(scope="module")
def certs_secret(namespace: str, issuer: str):
    return create_tls_certs(issuer, namespace, "test-tls-base-rs", "certs")


@fixture(scope="module")
def replica_set(issuer_ca_configmap: str, namespace: str, certs_secret) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("test-tls-base-rs.yaml"), namespace=namespace
    )
    resource.configure_custom_tls(issuer_ca_configmap, certs_secret)
    return resource.create()


@mark.e2e_replica_set_tls_default
def test_replica_set(replica_set: MongoDB, namespace: str):

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_tls_default
def test_file_has_correct_permissions(replica_set: MongoDB, namespace: str):
    # We test that the permissions are as expected by executing the stat
    # command on all the pem files in the secrets/certs directory
    cmd = [
        "/bin/sh",
        "-c",
        'stat -c "%a" /var/lib/mongodb-automation/secrets/certs/..*/*pem',
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"test-tls-base-rs-{i}", namespace, cmd,
        ).splitlines()
        for res in result:
            assert (
                res == "640"
            )  # stat has no option for decimal values, so we check for 640, which is the octal representation for 416
