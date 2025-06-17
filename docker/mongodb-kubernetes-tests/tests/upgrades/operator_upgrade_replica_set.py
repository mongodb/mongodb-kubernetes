from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_default_operator

RS_NAME = "my-replica-set"
USER_PASSWORD = "/qwerty@!#:"
CERT_PREFIX = "prefix"

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def rs_certs_secret(namespace: str, issuer: str):
    return create_mongodb_tls_certs(issuer, namespace, RS_NAME, "{}-{}-cert".format(CERT_PREFIX, RS_NAME))


@fixture(scope="module")
def replica_set(
    namespace: str,
    issuer_ca_configmap: str,
    rs_certs_secret: str,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set.yaml"),
        namespace=namespace,
        name=RS_NAME,
    )
    resource.set_version(custom_mdb_version)

    # Make sure we persist in order to be able to upgrade gracefully
    # and it is also faster.
    resource["spec"]["persistent"] = True

    # TLS
    resource.configure_custom_tls(
        issuer_ca_configmap,
        CERT_PREFIX,
    )

    # SCRAM-SHA
    resource["spec"]["security"]["authentication"] = {
        "enabled": True,
        "modes": ["SCRAM"],
    }

    return resource.create()


@fixture(scope="module")
def replica_set_user(replica_set: MongoDB) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("scram-sha-user.yaml"),
        namespace=replica_set.namespace,
        name="rs-user",
    )
    resource["spec"]["mongodbResourceRef"]["name"] = replica_set.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = "rs-user-password"
    resource["spec"]["username"] = "rs-user"

    print(f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} ")
    KubernetesTester.create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

    yield resource.create()


@mark.e2e_operator_upgrade_replica_set
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@mark.e2e_operator_upgrade_replica_set
def test_install_replicaset(replica_set: MongoDB):
    replica_set.assert_reaches_phase(phase=Phase.Running)


@mark.e2e_operator_upgrade_replica_set
def test_replicaset_user_created(replica_set_user: MongoDBUser):
    replica_set_user.assert_reaches_phase(Phase.Updated)


@mark.e2e_operator_upgrade_replica_set
def test_upgrade_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(
        namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
    )
    operator.assert_is_running()


@mark.e2e_operator_upgrade_replica_set
def test_replicaset_reconciled(replica_set: MongoDB):
    replica_set.assert_abandons_phase(phase=Phase.Running, timeout=300)
    replica_set.assert_reaches_phase(phase=Phase.Running, timeout=800)


@mark.e2e_operator_upgrade_replica_set
def test_replicaset_connectivity(replica_set: MongoDB, ca_path: str):
    tester = replica_set.tester(use_ssl=True, ca_path=ca_path)
    tester.assert_connectivity()

    # TODO refactor tester to flexibly test tls + custom CA + scram
    # tester.assert_scram_sha_authentication(
    #     password=USER_PASSWORD,
    #     username="rs-user",
    #     auth_mechanism="SCRAM-SHA-256")
