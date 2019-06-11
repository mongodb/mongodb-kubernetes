import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester

mdb_resource = "test-tls-base-sc-require-ssl"


def host_groups():
    "Returns the list of generated certs we use with this deployment"
    shard0 = ["{}-0-{}".format(mdb_resource, i) for i in range(3)]
    config = ["{}-config-{}".format(mdb_resource, i) for i in range(3)]
    mongos = ["{}-mongos-{}".format(mdb_resource, i) for i in range(2)]
    return dict(shards=shard0, mongos=mongos, config=config)


@pytest.mark.e2e_sharded_cluster_tls_require
class TestClusterWithTLSCreation(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Failed
      state because of missing certificates.
    create:
      file: test-tls-base-sc-require-ssl.yaml
      wait_until: in_failed_state
      timeout: 120
    """

    def test_custom_object_exists(self):
        assert self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )
        assert (
                mdb["status"]["message"]
                == "Not all certificates have been approved by Kubernetes CA"
        )


@pytest.mark.e2e_sharded_cluster_tls_require
class TestClusterWithTLSCreationApproval(KubernetesTester):
    """
    name: Approval of certificates
    description: |
      Approves the certificates in Kubernetes, the MongoDB resource should move to Successful state.
    """

    def setup(self):
        for _, hosts in host_groups().items():
            for host in hosts:
                self.approve_certificate("{}.{}".format(host, self.namespace))

    def test_noop(self):
        assert True


@pytest.mark.e2e_sharded_cluster_tls_require
class TestClusterWithTLSCreationRunning(KubernetesTester):
    """
    name: check everything is in place
    update:
      patch: '{}'
      file: test-tls-base-sc-require-ssl.yaml
      wait_until: in_running_state
      timeout: 360
    """

    def test_mdb_should_reach_goal_state(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )
        assert mdb["status"]["phase"] == "Running"

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self):
        mongo_tester = ShardedClusterTester("test-tls-base-sc-require-ssl", len(host_groups()["mongos"]), ssl=True)
        mongo_tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self):
        mongo_tester = ShardedClusterTester("test-tls-base-sc-require-ssl", len(host_groups()["mongos"]))
        mongo_tester.assert_no_connection()


@pytest.mark.e2e_sharded_cluster_tls_require
class TestClusterWithTLSCreationRemove(KubernetesTester):
    """
    name: Removal of TLS enabled Sharded Cluster
    description: |
      Removes TLS enabled Sharded Cluster
    delete:
      file: test-tls-base-sc-require-ssl.yaml
      wait_until: mongo_resource_deleted
      timeout: 120
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        for _, hosts in host_groups().items():
            for host in hosts:
                self.certificates.delete_certificate_signing_request(
                    "{}.{}".format(host, self.namespace), body=body
                )

    def test_deletion(self):
        assert True
