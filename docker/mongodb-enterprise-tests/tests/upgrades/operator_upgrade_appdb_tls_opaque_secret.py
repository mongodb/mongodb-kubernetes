from datetime import datetime

from kubetester import create_secret, get_pod_when_running, random_k8s_name, read_secret
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase, generic_replicaset
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

"""
This test is the same as `e2e_operator_upgrade_appdb_tls` but it uses a
certificate with concatenated key and certs, with type Opaque.

This test should be removed when v1.12.0 is EOL, which is going to be when
we reach EOL_FOR_V1_12. That day this test will start failing.

Steps forward:

- Make sure Opaque certificates are not supported anymore
- Opaque certificate support is removed from codebase
- Any sample files with Opaque certificates need to be deleted/updated
- Make sure release notes for next release include pointers at documentation
  for how to move from Opaque to tls.io certificates
- Remove this E2E test
"""


EOL_FOR_V1_12 = datetime(year=2022, month=4, day=20)
CERT_PREFIX = "prefix"


def certs_for_rs(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = random_k8s_name("rs-") + "-cert"
    concatenated_secret = "{}-{}-cert".format(CERT_PREFIX, resource_name)

    tls_secret_name = create_mongodb_tls_certs(
        issuer,
        namespace,
        resource_name,
        secret_name,
    )
    certs = read_secret(namespace, tls_secret_name)
    pem_cert = certs["tls.key"] + certs["tls.crt"]
    list_of_certs = {f"{resource_name}-{idx}-pem": pem_cert for idx in range(3)}

    # creates a Opaque secret with the concatenated PEM certificates.
    create_secret(
        namespace,
        concatenated_secret,
        list_of_certs,
    )
    return concatenated_secret


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    return certs_for_rs(issuer, namespace, "om-appdb-upgrade-tls-db")


@fixture(scope="module")
def replicaset_certs_secret(namespace: str, issuer: str):
    return certs_for_rs(issuer, namespace, "replica-set-tls")


@fixture(scope="module")
def ops_manager(
    namespace,
    issuer_ca_configmap: str,
    appdb_certs_secret: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_upgrade_tls.yaml"), namespace=namespace
    )

    resource["spec"]["applicationDatabase"]["security"]["tls"] = {
        "ca": issuer_ca_configmap,
        "secretRef": {"prefix": CERT_PREFIX},
    }
    return resource.create()


@fixture(scope="module")
def replicaset(
    namespace: str,
    issuer: str,
    issuer_ca_configmap: str,
    ops_manager: MongoDBOpsManager,
    replicaset_certs_secret: str,
) -> MongoDB:
    name = "replica-set-tls"
    rs = generic_replicaset(namespace, "4.4.9", name, ops_manager)
    rs["spec"]["security"] = {
        "tls": {
            "ca": issuer_ca_configmap,
            "enabled": True,
            "secretRef": {
                "prefix": CERT_PREFIX,
            },
        },
    }

    return rs.create()


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_operator_v12_is_still_supported():
    """
    Will fail when v12 is not supported anymore.

    EOL date obtained from: https://docs.google.com/spreadsheets/d/1x5vfesgCaGJbFI07OPNRgOAxSIZIAxrJcRME8qjuvcw
    """

    if datetime.now() > EOL_FOR_V1_12:
        assert (
            False
        ), "Operator Version 1.12.0 has been deprecated. === THIS TEST SHOULD BE REMOVED ==="


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_install_operator_v12(official_operator_v12: Operator, namespace: str):
    official_operator_v12.assert_is_running()

    # make sure this is actually version 12
    operator_pod = get_pod_when_running(
        namespace, "app.kubernetes.io/component=controller"
    )

    # Depending on the variant we might be fetching the image from Quay or Redhat,
    # but both of them ends with "enterprise-operator:1.12.0"
    assert operator_pod.spec.containers[0].image.endswith("enterprise-operator:1.12.0")


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    # appdb rolling restart for configuring monitoring
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_install_replica_set_before_upgrade(replicaset: MongoDB):
    replicaset.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_upgrade_operator(default_operator: Operator, version_id: str, namespace: str):
    default_operator.assert_is_running()

    # verify a new version has been updated
    operator_pod = get_pod_when_running(
        namespace, "app.kubernetes.io/component=controller"
    )
    assert operator_pod.spec.containers[0].image.endswith(version_id)


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_om_ok(ops_manager: MongoDBOpsManager):
    # status phases are updated gradually - we need to check for each of them (otherwise "check(Running) for OM"
    # will return True right away
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)

    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_check_replicaset_after_upgrade(replicaset: MongoDB):
    replicaset.assert_reaches_phase(Phase.Running, ignore_errors=True)
