import time

import pytest
from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubetester import create_or_update_secret, find_fixture
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase

USER_PASSWORD = "my-password"
RESOURCE_NAME = "my-replica-set"


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("replica-set-scram-sha-256.yaml"), namespace=namespace)
    res.set_version(custom_mdb_version)
    return res.update()


@pytest.fixture(scope="module")
def scram_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(find_fixture("user_scram.yaml"), namespace=namespace)

    create_or_update_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {"password": USER_PASSWORD},
    )

    return resource.update()


@pytest.mark.e2e_users_finalizer
class TestReplicaSetIsRunning(KubernetesTester):

    def test_mdb_resource_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_users_finalizer
class TestUserIsAdded(KubernetesTester):

    def test_user_is_ready(mdb: MongoDB, scram_user: MongoDBUser):
        scram_user.assert_reaches_phase(Phase.Updated)

        ac = KubernetesTester.get_automation_config()
        assert len(ac["auth"]["usersWanted"]) == 1

    def test_users_are_added_to_automation_config(self):
        ac = KubernetesTester.get_automation_config()

        assert ac["auth"]["usersWanted"][0]["user"] == "username"

    def test_user_has_finalizer(self, scram_user: MongoDBUser):
        scram_user.load()
        finalizers = scram_user["metadata"]["finalizers"]

        assert finalizers[0] == "mongodb.com/v1.userRemovalFinalizer"


@pytest.mark.e2e_users_finalizer
class TestTheDeletedUserRemainsInCluster(KubernetesTester):

    def test_deleted_user_has_deletion_timestamp(self):
        resource = MongoDBUser.from_yaml(find_fixture("user_scram.yaml"), namespace="mongodb-test")
        resource.load()
        resource.delete()
        resource.reload()

        finalizers = resource["metadata"]["finalizers"]

        assert finalizers[0] == "mongodb.com/v1.userRemovalFinalizer"
        assert resource["metadata"]["deletionTimestamp"] != None


@pytest.mark.e2e_users_finalizer
class TestCleanupIdPerformedBeforeDeletingUser(KubernetesTester):
    """
    delete:
      file: user_scram.yaml
      wait_until: finalizer_is_removed
    """

    @staticmethod
    def finalizer_is_removed():
        resource = MongoDBUser.from_yaml(find_fixture("user_scram.yaml"), namespace="mongodb-test")
        try:
            resource.load()
        except ApiException:
            return True

        return resource["metadata"]["finalizers"] == []

    def test_deleted_user_is_removed_from_automation_config(self):
        ac = KubernetesTester.get_automation_config()
        users = ac["auth"]["usersWanted"]
        assert "username" not in [user["user"] for user in users]
