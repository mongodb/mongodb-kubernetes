import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE = "my-replica-set"
USER_NAME = "mms-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


@fixture(scope="module")
def scram_user(namespace) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(yaml_fixture("scram-sha-user.yaml"), namespace=namespace)

    print(f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} ")
    KubernetesTester.create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

    return resource


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-basic.yaml"),
        namespace=namespace,
        name="my-replica-set",
    )

    return resource


@pytest.mark.e2e_replica_set_scram_sha_256_user_first
def test_replica_set_created(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_replica_set_scram_sha_256_user_first
def test_user_pending(scram_user: MongoDBUser):
    """pending phase as auth has not yet been enabled"""
    scram_user.update()
    scram_user.assert_reaches_phase(Phase.Pending, timeout=50)


@pytest.mark.e2e_replica_set_scram_sha_256_user_first
def test_replica_set_auth_enabled(replica_set: MongoDB):
    replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@pytest.mark.e2e_replica_set_scram_sha_256_user_first
def test_user_created(scram_user: MongoDBUser):
    scram_user.assert_reaches_phase(Phase.Updated, timeout=50)


@pytest.mark.e2e_replica_set_scram_sha_256_user_first
def test_replica_set_connectivity(replica_set: MongoDB):
    replica_set.assert_connectivity()


@pytest.mark.e2e_replica_set_scram_sha_256_user_first
def test_ops_manager_state_correctly_updated(replica_set: MongoDB):
    tester = replica_set.get_automation_config_tester()
    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
    tester.assert_authentication_enabled()
    tester.assert_expected_users(1)
    tester.assert_authoritative_set(True)
