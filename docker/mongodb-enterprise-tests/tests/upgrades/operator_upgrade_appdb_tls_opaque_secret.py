from kubetester import create_secret, random_k8s_name, read_secret
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase, generic_replicaset
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

"""
This test is the same as `e2e_operator_upgrade_appdb_tls` but it uses a
certificate with concatenated key and certs, with type Opaque.
"""


def certs_for_rs(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = random_k8s_name("rs-") + "-cert"
    concatenated_secret = secret_name + "-secret"

    tls_secret_name = create_mongodb_tls_certs(
        issuer, namespace, resource_name, secret_name,
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

    resource["spec"]["applicationDatabase"]["security"]["tls"]["secretRef"]["name"] = appdb_certs_secret
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
                "name": replicaset_certs_secret,
            }
        },
    }
    return rs.create()


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_install_operator_v12(official_operator_v12: Operator):
    official_operator_v12.assert_is_running()


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    # appdb rolling restart for configuring monitoring
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@skip_if_local
@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_install_replica_set_before_upgrade(replicaset: MongoDB):
    replicaset.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_upgrade_appdb_tls_opaque_secret
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


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
    replicaset.assert_reaches_phase(Phase.Running)
