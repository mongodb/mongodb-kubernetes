from datetime import datetime

from kubetester import create_secret, get_pod_when_running, random_k8s_name, read_secret
from kubetester.certs import (
    create_mongodb_tls_certs,
    create_agent_tls_certs,
    SetProperties,
)
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import (
    MongoDB,
    Phase,
    generic_replicaset,
    generic_shardedcluster,
)
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

"""
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


EOL_FOR_V1_12 = datetime(year=2022, month=5, day=20)
CERT_PREFIX = "prefix"
NEW_CERT_PREFIX = "new-prefix"
MDB_RESOURCE_NAME = "replica-set-tls"
SC_RESOURCE_NAME = "sharded-cluster-tls"
SUBJECT = {"organizations": ["MDB Tests"], "organizationalUnits": ["Servers"]}
SERVER_SETS = frozenset(
    [
        SetProperties(SC_RESOURCE_NAME + "-0", SC_RESOURCE_NAME + "-sh", 3),
        SetProperties(SC_RESOURCE_NAME + "-config", SC_RESOURCE_NAME + "-cs", 3),
        SetProperties(SC_RESOURCE_NAME + "-mongos", SC_RESOURCE_NAME + "-svc", 2),
    ]
)


def certs_for_rs(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = f"{NEW_CERT_PREFIX}-{resource_name}-cert"
    concatenated_secret = "{}-{}-cert".format(CERT_PREFIX, resource_name)

    tls_secret_name = create_mongodb_tls_certs(
        issuer,
        namespace,
        resource_name,
        secret_name,
    )
    certs = read_secret(namespace, tls_secret_name)
    pem_cert = certs["tls.crt"] + certs["tls.key"]
    list_of_certs = {f"{resource_name}-{idx}-pem": pem_cert for idx in range(3)}
    # creates a Opaque secret with the concatenated PEM certificates.
    create_secret(
        namespace,
        concatenated_secret,
        list_of_certs,
    )

    return concatenated_secret


def certs_for_sc(issuer: str, namespace: str, resource_name: str) -> None:
    spec = {
        "subject": SUBJECT,
        "usages": ["server auth", "client auth"],
    }
    for server_set in SERVER_SETS:
        secret_name = f"{NEW_CERT_PREFIX}-{server_set.name}-cert"
        concatenated_secret = f"{CERT_PREFIX}-{server_set.name}-cert"
        tls_secret_name = create_mongodb_tls_certs(
            issuer,
            namespace,
            server_set.name,
            secret_name,
            server_set.replicas,
            server_set.service,
            spec,
        )
        certs = read_secret(namespace, tls_secret_name)
        pem_cert = certs["tls.crt"] + certs["tls.key"]
        list_of_certs = {f"{server_set.name}-{idx}-pem": pem_cert for idx in range(3)}
        create_secret(
            namespace,
            concatenated_secret,
            list_of_certs,
        )


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    return certs_for_rs(issuer, namespace, "om-appdb-upgrade-tls-db")


@fixture(scope="module")
def replicaset_certs_secret(namespace: str, issuer: str):
    return certs_for_rs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def shardedcluster_certs_secret(namespace: str, issuer: str):
    return certs_for_sc(issuer, namespace, SC_RESOURCE_NAME)


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
        "enabled": True,
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
    name = MDB_RESOURCE_NAME
    rs = generic_replicaset(namespace, "4.4.9", name, ops_manager)
    rs["spec"]["security"] = {
        "tls": {
            "ca": issuer_ca_configmap,
            "enabled": True,
            "secretRef": {
                "prefix": CERT_PREFIX,
            },
        },
        "authentication": {
            "enabled": True,
            "modes": ["SCRAM"],
            "agents": {"mode": "SCRAM"},
        },
    }

    return rs.create()


@fixture(scope="module")
def shardedcluster(
    namespace: str,
    issuer: str,
    issuer_ca_configmap: str,
    ops_manager: MongoDBOpsManager,
    shardedcluster_certs_secret: str,
) -> MongoDB:
    name = SC_RESOURCE_NAME
    sc = generic_shardedcluster(namespace, "4.4.9", name, ops_manager)
    sc["spec"]["security"] = {
        "tls": {
            "ca": issuer_ca_configmap,
            "enabled": True,
            "secretRef": {
                "prefix": CERT_PREFIX,
            },
        },
        "authentication": {
            "enabled": True,
            "modes": ["SCRAM"],
            "agents": {"mode": "SCRAM"},
        },
    }

    return sc.create()


@mark.e2e_operator_upgrade_automated_tls
def test_operator_v12_is_still_supported():
    """
    Will fail when v12 is not supported anymore.

    EOL date obtained from: https://docs.google.com/spreadsheets/d/1x5vfesgCaGJbFI07OPNRgOAxSIZIAxrJcRME8qjuvcw
    """

    if datetime.now() > EOL_FOR_V1_12:
        assert (
            False
        ), "Operator Version 1.12.0 has been deprecated. === THIS TEST SHOULD BE REMOVED ==="


@mark.e2e_operator_upgrade_automated_tls
def test_install_operator_v12(official_operator_v12: Operator, namespace: str):
    official_operator_v12.assert_is_running()

    # make sure this is actually version 12
    operator_pod = get_pod_when_running(
        namespace, "app.kubernetes.io/component=controller"
    )

    # Depending on the variant we might be fetching the image from Quay or Redhat,
    # but both of them ends with "enterprise-operator:1.12.0"
    assert operator_pod.spec.containers[0].image.endswith("enterprise-operator:1.12.0")


@mark.e2e_operator_upgrade_automated_tls
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    # appdb rolling restart for configuring monitoring
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_operator_upgrade_automated_tls
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_automated_tls
def test_install_replica_set_before_upgrade(replicaset: MongoDB):
    replicaset.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_upgrade_automated_tls
def test_install_sharded_cluster_before_upgrade(shardedcluster: MongoDB):
    shardedcluster.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_operator_upgrade_automated_tls
def test_upgrade_operator(default_operator: Operator, namespace: str, version_id: str):
    default_operator.assert_is_running()

    # verify a new version has been updated
    operator_pod = get_pod_when_running(
        namespace, "app.kubernetes.io/component=controller"
    )
    assert operator_pod.spec.containers[0].image.endswith(version_id)


@mark.e2e_operator_upgrade_automated_tls
def test_om_ok(ops_manager: MongoDBOpsManager):
    # status phases are updated gradually - we need to check for each of them (otherwise "check(Running) for OM"
    # will return True right away
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_automated_tls
def test_replicaset_reconciles_after_upgrade(replicaset: MongoDB):
    replicaset.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_upgrade_automated_tls
def test_shardedcluster_reconciles_after_upgrade(shardedcluster: MongoDB):
    shardedcluster.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_upgrade_automated_tls
def test_om_change_appdb_cert_reference(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["security"]["tls"]["secretRef"] = None
    ops_manager["spec"]["applicationDatabase"]["security"]["tls"]["enabled"] = None
    ops_manager["spec"]["applicationDatabase"]["security"][
        "certsSecretPrefix"
    ] = NEW_CERT_PREFIX
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)

    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_automated_tls
@mark.skip
def test_change_replicaset_cert_reference(replicaset: MongoDB):
    replicaset.load()
    replicaset["spec"]["security"]["tls"]["secretRef"] = None
    replicaset["spec"]["security"]["certsSecretPrefix"] = NEW_CERT_PREFIX
    replicaset.update()

    replicaset.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_operator_upgrade_automated_tls
@mark.skip
def test_change_shardedcluster_cert_reference(shardedcluster: MongoDB):
    shardedcluster.load()
    shardedcluster["spec"]["security"]["tls"]["secretRef"] = None
    shardedcluster["spec"]["security"]["certsSecretPrefix"] = NEW_CERT_PREFIX
    shardedcluster.update()

    shardedcluster.assert_reaches_phase(Phase.Running, timeout=1200)
