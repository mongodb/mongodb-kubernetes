import pytest

from kubernetes import client
from kubetester.kubetester import KubernetesTester, skip_if_local
from kubetester.mongotester import ShardedClusterTester

from kubetester.crypto import (
    generate_csr,
    request_certificate,
    get_pem_certificate,
    wait_for_certs_to_be_issued,
)

from typing import Dict, List

MDB_RESOURCE = "test-tls-base-sc-require-ssl"
usages = ["digital signature", "key encipherment", "server auth", "client auth"]


def servicename_for_group(group_name: str) -> str:
    groups = {
        f"{MDB_RESOURCE}-0": f"{MDB_RESOURCE}-sh",
        f"{MDB_RESOURCE}-config": f"{MDB_RESOURCE}-cs",
        f"{MDB_RESOURCE}-mongos": f"{MDB_RESOURCE}-svc",
    }

    return groups[group_name]


# TODO: There's an almost equivalent functionality in omtester. Make sure you use just one.
def host_groups() -> Dict[str, List[str]]:
    "Returns the list of generated certs we use with this deployment"
    shard0 = ["{}-0-{}".format(MDB_RESOURCE, i) for i in range(3)]
    config = ["{}-config-{}".format(MDB_RESOURCE, i) for i in range(3)]
    mongos = ["{}-mongos-{}".format(MDB_RESOURCE, i) for i in range(2)]
    return {
        f"{MDB_RESOURCE}-0": shard0,
        f"{MDB_RESOURCE}-config": config,
        f"{MDB_RESOURCE}-mongos": mongos,
    }


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreateCerts(KubernetesTester):
    @classmethod
    def setup_env(cls):
        cls.keys = {}

        for name, group in host_groups().items():
            for pod_name in group:
                csr, key = generate_csr(
                    cls.get_namespace(), pod_name, servicename_for_group(name)
                )
                cls.keys[pod_name] = key
                cert_name = "{}.{}".format(pod_name, cls.get_namespace())
                request_certificate(csr, cert_name, usages)

        # Creates the "customer-ca" with an existing CA.
        data = cls.read_configmap("default", "ca-certificates")
        cls.create_configmap(cls.get_namespace(), "customer-ca", data)

    def test_approve_certs(self):
        certs = []
        for name, group in host_groups().items():
            for host in group:
                certs.append("{}.{}".format(host, self.get_namespace()))

        for cert in self.yield_existing_csrs(certs):
            self.approve_certificate(cert)

        wait_for_certs_to_be_issued(certs)

    def test_create_secrets(self):
        for name, group in host_groups().items():
            server_certs: Dict[str, [bytes]] = dict()
            for pod_name in group:
                cert = get_pem_certificate(
                    "{}.{}".format(pod_name, self.get_namespace())
                )
                server_certs[f"{pod_name}-pem"] = (cert + self.keys[pod_name]).decode(
                    "utf-8"
                )

            secret_name = f"{name}-cert"
            KubernetesTester.create_secret(
                self.get_namespace(), secret_name, server_certs
            )


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreation(KubernetesTester):
    """
    name: Sharded Cluster With TLS Creation
    description: |
      Creates a MongoDB object with the ssl attribute on. The MongoDB object will go to Running
      state because secrets with certificates have been created already.
    create:
      file: test-tls-base-sc-require-ssl-custom-ca.yaml
      wait_until: in_running_state
    """

    def test_mdb_resource_status_is_running(self):
        assert KubernetesTester.get_resource()["status"]["phase"] == "Running"

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self):
        mongo_tester = ShardedClusterTester(
            MDB_RESOURCE, len(host_groups()[f"{MDB_RESOURCE}-mongos"]), ssl=True
        )
        mongo_tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self):
        mongo_tester = ShardedClusterTester(
            MDB_RESOURCE, len(host_groups()[f"{MDB_RESOURCE}-mongos"])
        )
        mongo_tester.assert_no_connection()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSAddMoreCerts(KubernetesTester):
    def test_add_2_more_certificates(self):
        KubernetesTester.test_keys = {}
        for i in range(3, 5):
            name = f"{MDB_RESOURCE}-0"
            pod_name = f"{name}-{i}"

            csr, key = generate_csr(
                self.get_namespace(), pod_name, servicename_for_group(name)
            )
            KubernetesTester.test_keys[pod_name] = key
            request_certificate(
                csr, "{}.{}".format(pod_name, self.get_namespace()), usages
            )

    def test_approve_certs(self):
        certs = []
        for i in range(3, 5):
            certs.append("{}-0-{}.{}".format(MDB_RESOURCE, i, self.get_namespace()))

        for cert in self.yield_existing_csrs(certs):
            self.approve_certificate(cert)

    def test_update_secrets(self):
        server_certs: Dict[str, [bytes]] = dict()
        for pod_name in [f"{MDB_RESOURCE}-0-{i}" for i in range(3, 5)]:
            cert = get_pem_certificate("{}.{}".format(pod_name, self.get_namespace()))
            server_certs[f"{pod_name}-pem"] = (
                cert + KubernetesTester.test_keys[pod_name]
            ).decode("utf-8")

            secret_name = f"{MDB_RESOURCE}-0-cert"
            KubernetesTester.update_secret(
                self.get_namespace(), secret_name, server_certs
            )


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreationRunning(KubernetesTester):
    """
    name: After scaling, with existing certs, the MDB object should reach running state.
    description:
    update:
      patch: '[{"op": "replace", "path": "/spec/mongodsPerShardCount", "value": 5}]'
      file: test-tls-base-sc-require-ssl-custom-ca.yaml
      wait_until: in_running_state
    """

    def test_mdb_should_reach_goal_state(self):
        assert self.get_resource()["status"]["phase"] == "Running"

    @skip_if_local
    def test_mongos_are_reachable_with_ssl(self):
        mongo_tester = ShardedClusterTester(
            "test-tls-base-sc-require-ssl",
            len(host_groups()[f"{MDB_RESOURCE}-mongos"]),
            ssl=True,
        )
        mongo_tester.assert_connectivity()

    @skip_if_local
    def test_mongos_are_not_reachable_with_no_ssl(self):
        mongo_tester = ShardedClusterTester(
            "test-tls-base-sc-require-ssl", len(host_groups()[f"{MDB_RESOURCE}-mongos"])
        )
        mongo_tester.assert_no_connection()


@pytest.mark.e2e_sharded_cluster_tls_require_custom_ca
class TestClusterWithTLSCreationRemove(KubernetesTester):
    """
    name: Removal of TLS enabled Sharded Cluster
    description: |
      Removes TLS enabled Sharded Cluster
    delete:
      file: test-tls-base-sc-require-ssl-custom-ca.yaml
      wait_until: mongo_resource_deleted
    """

    def setup(self):
        # Deletes the certificate
        body = client.V1DeleteOptions()
        for _, hosts in host_groups().items():
            for host in hosts:
                self.certificates.delete_certificate_signing_request(
                    "{}.{}".format(host, self.get_namespace()), body=body
                )

    def test_deletion(self):
        assert True
