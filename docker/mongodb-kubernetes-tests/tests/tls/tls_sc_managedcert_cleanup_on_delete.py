"""Deleting a sharded cluster that uses operator-managed certificates deletes what the feature created
for it, so a resource created afterwards with the same name is issued fresh certificates.

The sharded cluster issues a member and a clusterfile certificate for every component (the mongos, the
config server and each shard), so it creates more than a replica set does, and through a different
controller. cert-manager writes the secrets it issues without an owner reference, so without the owner
the operator sets on them, they outlive the resource. The next Certificate with the same name then
adopts the old secret, because cert-manager compares a Certificate with its secret by name only.
"""

import pytest
from kubetester import read_secret, try_load, wait_until
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    assert_all_exist,
    is_gone,
    leaf_secret_names_for,
    managed_cert_objects,
    managed_certificate_operator,
)

MDB_RESOURCE = "test-tls-sc-managedcert-cleanup"
SHARD_COUNT = 1

# The leaf certificates the first deployment was issued, to compare with the ones the second gets.
first_leaf_certificates: dict[str, str] = {}


def components() -> list[str]:
    """The names the member and clusterfile certificates are issued for: one per component."""
    components = [f"{MDB_RESOURCE}-mongos", f"{MDB_RESOURCE}-config"]
    return components + [f"{MDB_RESOURCE}-{i}" for i in range(SHARD_COUNT)]


def managed_cert_sharded_cluster(namespace: str, mdb_version: str) -> MongoDB:
    """A TLS sharded cluster with X509 member and internal auth, using operator-managed certificates.

    It runs without persistent storage, so the second deployment starts from empty disks and nothing of
    the first one is left but what this test checks for.
    """
    resource = MongoDB.from_yaml(load_fixture("test-tls-sc-managedcert.yaml"), name=MDB_RESOURCE, namespace=namespace)
    resource.set_version(mdb_version)
    resource["spec"]["shardCount"] = SHARD_COUNT
    resource["spec"]["persistent"] = False
    # No issuer for either category: the operator self-signs both the server and the client CAs.
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    return managed_cert_sharded_cluster(namespace, custom_mdb_version)


def dependent_k8s_resources() -> list[tuple[str, str]]:
    """Everything the feature creates for the resource, in the operator's cluster."""
    return managed_cert_objects(MDB_RESOURCE, components())


@pytest.mark.e2e_sharded_cluster_tls_managedcert_cleanup_on_delete
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_sharded_cluster_tls_managedcert_cleanup_on_delete
def test_sharded_cluster_running(sc: MongoDB, namespace: str):
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)

    for name in leaf_secret_names_for(MDB_RESOURCE, components()):
        first_leaf_certificates[name] = read_secret(namespace, name)["tls.crt"]

    assert_all_exist(dependent_k8s_resources(), namespace)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_cleanup_on_delete
# Deleting the resource removes everything the feature created for it: every object in the list has to
# be gone before the test passes.
def test_cleanup(sc: MongoDB, namespace: str):
    sc.delete()

    def everything_is_gone() -> bool:
        objects = [("mongodb", MDB_RESOURCE)] + dependent_k8s_resources()
        return all(is_gone(kind, name, namespace) for kind, name in objects)

    # Everything is in the operator's cluster and carries the resource as its owner, so Kubernetes
    # deletes it together with the resource.
    wait_until(everything_is_gone, timeout=300)


@pytest.mark.e2e_sharded_cluster_tls_managedcert_cleanup_on_delete
def test_a_resource_with_the_same_name_gets_fresh_certificates(namespace: str, custom_mdb_version: str):
    # A new object: the one the fixture returns still refers to the deleted resource, so updating it
    # would patch a resource that is not there.
    sc = managed_cert_sharded_cluster(namespace, custom_mdb_version)
    sc.update()
    sc.assert_reaches_phase(Phase.Running, timeout=1200)

    # The feature created its whole set again for the new resource, not only the certificates below.
    assert_all_exist(dependent_k8s_resources(), namespace)

    for name in leaf_secret_names_for(MDB_RESOURCE, components()):
        assert (
            read_secret(namespace, name)["tls.crt"] != first_leaf_certificates[name]
        ), f"{name} still holds the certificate of the deleted deployment, so cert-manager found the secret it left behind"
