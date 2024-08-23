import time

import pytest
from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubetester import create_or_update, create_or_update_secret, find_fixture
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser

USER_PASSWORD = "my-password"
RESOURCE_NAME = "my-replica-set"


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    res = MongoDB.from_yaml(load_fixture("replica-set-scram-sha-256.yaml"), namespace=namespace)
    res.set_version(custom_mdb_version)
    return create_or_update(res)


@pytest.fixture(scope="module")
def scram_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(find_fixture("user_scram.yaml"), namespace=namespace)

    create_or_update_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {"password": USER_PASSWORD},
    )

    return create_or_update(resource)


@pytest.mark.e2e_users_finalizer_removal
class TestReplicaSetIsRunning(KubernetesTester):

    def test_mdb_resource_running(self, mdb: MongoDB):
        mdb.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_users_finalizer_removal
class TestUserIsAdded(KubernetesTester):

    def test_user_is_ready(mdb: MongoDB, scram_user: MongoDBUser):
        scram_user.assert_reaches_phase(Phase.Updated)

        ac = KubernetesTester.get_automation_config()
        assert len(ac["auth"]["usersWanted"]) == 1


@pytest.mark.e2e_users_finalizer_removal
class TestReplicaSetIsDleted(KubernetesTester):
    """
    delete:
        file: replica-set-scram-sha-256.yaml
        wait_until: mongo_resource_deleted
    """

    def test_replica_set_sts_doesnt_exist(self):
        """The StatefulSet must be removed by Kubernetes as soon as the MongoDB resource is removed.
        Note, that this may lag sometimes (caching or whatever?) and it's more safe to wait a bit"""
        time.sleep(15)
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set(RESOURCE_NAME, self.namespace)

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service(RESOURCE_NAME + "-svc", self.namespace)


@pytest.mark.e2e_users_finalizer_removal
class TestUserIsRemovedAfterMongoDBIsDeleted(KubernetesTester):
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

    def test_user_is_deleted(self):
        ac = KubernetesTester.get_automation_config()
        assert len(ac["auth"]["usersWanted"]) == 0
