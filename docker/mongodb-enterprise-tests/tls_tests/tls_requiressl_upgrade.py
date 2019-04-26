import pytest

import sys
import os
import os.path


try:
    from kubetester import KubernetesTester, skip_if_local, build_list_of_hosts
except ImportError:
    # patching python import path so it finds kubetester
    sys.path.append(os.path.dirname(os.getcwd()))
    from kubetester import KubernetesTester, skip_if_local, build_list_of_hosts

from kubernetes import client


mdb_resource = "test-tls-upgrade"


def host_names():
    return ["{}-{}".format(mdb_resource, i) for i in range(3)]


@pytest.mark.tls_base_upgrade
class TestReplicaSetWithTLSUpgradeCreation(KubernetesTester):
    """
    name: Replica Set Upgraded to requireSSL
    description: |
      Creates a mdb resource with type "ReplicaSet" with no SSL enabled, mean to
      be "upgraded" to requireSSL in next phase
    create:
      file: fixtures/test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: in_running_state
      timeout: 120
    """

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
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
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=120)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.tls_base_upgrade
class TestReplicaSetWithTLSUpgradeSetRequireSSLMode(KubernetesTester):
    """
    name: Upgrade the Replica Set to `requireSSL` and check it goes to "Failed" state.
    description: |
      Upgrades the Replica Set to `requireSSL`. The Replica Set will move to "Failed" state,
      while still operational (we won't break the ReplicaSet for now, it will be just not upgraded).
    update:
      file: fixtures/test-tls-base-rs-require-ssl-upgrade.yaml
      patch: '[{"op": "add", "path" : "/spec/security", "value": {}}, {"op":"add","path":"/spec/security/tls","value": { "enabled": true }}]'
      wait_until: in_failed_state
      timeout: 240
    """

    def test_mdb_resource_status_is_correct(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )
        assert (
            mdb["status"]["message"]
            == "Not all certificates have been approved by Kubernetes CA"
        )

    @skip_if_local()
    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=20)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.tls_base_require
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


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSUpgradeRunning(KubernetesTester):
    """
    name: check everything is in place
    noop:
      wait_until: in_running_state
      timeout: 240
    """

    def test_mdb_should_reach_goal_state(self):
        mdb = self.customv1.get_namespaced_custom_object(
            "mongodb.com", "v1", self.namespace, "mongodb", mdb_resource
        )
        assert mdb["status"]["phase"] == "Running"

    @skip_if_local()
    @pytest.mark.xfail
    def test_mdb_is_reachable_with_no_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, wait_for=20)

        assert primary is not None
        assert len(secondaries) == 2

    @skip_if_local()
    def test_mdb_is_reachable_with_ssl(self):
        hosts = build_list_of_hosts(mdb_resource, self.namespace, 3)
        primary, secondaries = self.wait_for_rs_is_ready(hosts, ssl=True)

        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.tls_base_require
class TestReplicaSetWithTLSCreationDeleted(KubernetesTester):
    """
    name: Removal of TLS enabled Replica Set
    description: |
      Removes TLS enabled Replica Set
    delete:
      file: fixtures/test-tls-base-rs-require-ssl-upgrade.yaml
      wait_until: mongo_resource_deleted
      timeout: 120
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        for host in host_names():
            self.certificates.delete_certificate_signing_request(
                "{}.{}".format(host, self.namespace), body
            )

    def test_deletion(self):
        assert True
