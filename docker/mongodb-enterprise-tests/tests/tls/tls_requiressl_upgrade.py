import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ReplicaSetTester

MDB_RESOURCE = "test-tls-upgrade"


def host_names():
    return ["{}-{}".format(MDB_RESOURCE, i) for i in range(3)]


@pytest.mark.e2e_replica_set_tls_require_upgrade
class TestReplicaSetWithTLSUpgradeCreation(KubernetesTester):
    """
    name: Replica Set Upgraded to requireSSL
    description: |
      Creates a mdb resource with type "ReplicaSet" with no SSL enabled, mean to
      be "upgraded" to requireSSL in next phase
    create:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: in_running_state
      timeout: 240
    """

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", MDB_RESOURCE
        )
        assert mdb["status"]["phase"] == "Running"
        assert mdb["status"]["type"] == "ReplicaSet"
        assert mdb["status"]["version"] == "4.0.0"
        assert mdb["status"]["members"] == 3

        # make sure the following attributes are set
        assert mdb["status"].get("link")

        # TODO: verify the next attribute is an actual timestamp
        assert mdb["status"].get("lastTransition")

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        mongo_tester = ReplicaSetTester(MDB_RESOURCE, 3)
        mongo_tester.assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_upgrade
class TestReplicaSetWithTLSUpgradeSetRequireSSLMode(KubernetesTester):
    """
    name: Upgrade the Replica Set to `requireSSL` and check it goes to "Failed" state.
    description: |
      Upgrades the Replica Set to `requireSSL`. The Replica Set will move to "Failed" state,
      while still operational (we won't break the ReplicaSet for now, it will be just not upgraded).
    update:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      patch: '[{"op": "add", "path" : "/spec/security", "value": {}}, {"op":"add","path":"/spec/security/tls","value": { "enabled": true }}]'
      wait_for_message: Not all certificates have been approved by Kubernetes CA
      timeout: 360
    """

    def test_mdb_resource_status_is_pending(self):
        assert KubernetesTester.get_resource()["status"]["phase"] == "Pending"

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_upgrade
class TestReplicaSetWithTLSUpgradeApproveCerts(KubernetesTester):
    """
    name: Approval of certificates
    description: |
      Approves the certificates in Kubernetes, the MongoDB resource should move to Successful state.
    """

    def setup(self):
        for host in host_names():
            self.approve_certificate("{}.{}".format(host, self.namespace))

    def test_noop(self):
        assert True


@pytest.mark.e2e_replica_set_tls_require_upgrade
class TestReplicaSetWithTLSUpgradeRunning(KubernetesTester):
    """
    name: check everything is in place
    noop:
      wait_until: in_running_state
      timeout: 360
    """

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_no_connection()

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        ReplicaSetTester(MDB_RESOURCE, 3, ssl=True).assert_connectivity()


@pytest.mark.e2e_replica_set_tls_require_upgrade
class TestReplicaSetWithTLSCreationDeleted(KubernetesTester):
    """
    name: Removal of TLS enabled Replica Set
    description: |
      Removes TLS enabled Replica Set
    delete:
      file: test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        for host in host_names():
            self.certificates.delete_certificate_signing_request(
                "{}.{}".format(host, self.namespace), body=body
            )

    def test_deletion(self):
        assert True
