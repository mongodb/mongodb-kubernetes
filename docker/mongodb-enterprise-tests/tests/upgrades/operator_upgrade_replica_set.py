import pytest
from kubetester import MongoDB
from kubetester.certs import create_tls_certs
from kubetester.mongodb import Phase
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from pytest import fixture

RS_NAME = "my-replica-set"
CA_PEM_FILE_PATH = "/var/run/secrets/ca-pem"
USER_PASSWORD = "/qwerty@!#:"


@fixture(scope="module")
def rs_certs_secret(namespace: str, issuer: str):
    return create_tls_certs(issuer, namespace, RS_NAME, "certs-for-replicaset")


@fixture(scope="module")
def replica_set(
    namespace: str,
    issuer_ca_configmap: str,
    rs_certs_secret: str,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set.yaml"), namespace=namespace, name=RS_NAME,
    )
    resource["spec"]["version"] = custom_mdb_version
    # TLS
    resource.configure_custom_tls(issuer_ca_configmap, rs_certs_secret)

    # SCRAM-SHA
    resource["spec"]["security"]["authentication"] = {
        "enabled": True,
        "modes": ["SCRAM"],
    }

    return resource.create()


@fixture(scope="module")
def replica_set_user(replica_set: MongoDB) -> MongoDBUser:
    """ Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("scram-sha-user.yaml"),
        namespace=replica_set.namespace,
        name="rs-user",
    )
    resource["spec"]["mongodbResourceRef"]["name"] = replica_set.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = "rs-user-password"
    resource["spec"]["username"] = "rs-user"

    print(
        f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} "
    )
    KubernetesTester.create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {"password": USER_PASSWORD,},
    )

    yield resource.create()


@pytest.mark.e2e_operator_upgrade_replica_set
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@pytest.mark.e2e_operator_upgrade_replica_set
def test_install_replicaset(replica_set: MongoDB):
    replica_set.assert_reaches_phase(phase=Phase.Running)


@pytest.mark.e2e_operator_upgrade_replica_set
def test_replicaset_user_created(replica_set_user: MongoDBUser):
    replica_set_user.assert_reaches_phase(Phase.Updated)


@pytest.mark.e2e_operator_upgrade_replica_set
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@pytest.mark.e2e_operator_upgrade_replica_set
def test_replicaset_reconciled(replica_set: MongoDB):
    replica_set.assert_abandons_phase(phase=Phase.Running, timeout=100)
    replica_set.assert_reaches_phase(phase=Phase.Running, timeout=400)


@pytest.mark.e2e_operator_upgrade_replica_set
def test_replicaset_connectivity(replica_set: MongoDB, issuer_ca_configmap: str):
    # Write the CA from ConfigMap to local file to test connectivity to database
    ca = KubernetesTester.read_configmap(replica_set.namespace, issuer_ca_configmap)[
        "ca-pem"
    ]
    with open(CA_PEM_FILE_PATH, "w") as f:
        f.write(ca)

    tester = replica_set.tester(insecure=False, ca_path=CA_PEM_FILE_PATH)
    tester.assert_connectivity()

    # TODO refactor tester to flexibly test tls + custom CA + scram
    # tester.assert_scram_sha_authentication(
    #     password=USER_PASSWORD,
    #     username="rs-user",
    #     auth_mechanism="SCRAM-SHA-256")
