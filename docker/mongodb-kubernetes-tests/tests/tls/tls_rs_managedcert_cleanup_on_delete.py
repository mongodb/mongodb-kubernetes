"""Deleting a MongoDB that uses operator-managed certificates deletes what the feature created for it,
so a resource created afterwards with the same name is issued a fresh certificate.

cert-manager writes the secret it issues without an owner reference, so without the owner the operator
sets on it, the secret outlives the resource. The next Certificate with the same name then adopts it,
because cert-manager compares a Certificate with its secret by name only, and the new deployment runs
with the certificate of the deleted one.
"""

import pytest
from kubetester import read_secret, wait_until
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.tls.managedcert_helper import (
    assert_all_exist,
    is_gone,
    leaf_secret_names_for,
    managed_cert_objects,
    managed_cert_replica_set,
    managed_certificate_operator,
)

MDB_RESOURCE = "test-tls-rs-managedcert-cleanup"

# The leaf certificates the first deployment was issued, to compare with the ones the second gets.
first_leaf_certificates: dict[str, str] = {}


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    # No issuer for either category: the operator self-signs both the server and the client CAs.
    return managed_cert_replica_set(MDB_RESOURCE, namespace, custom_mdb_version, {})


def dependent_k8s_resources() -> list[tuple[str, str]]:
    """Everything the feature creates for the resource, in the operator's cluster."""
    return managed_cert_objects(MDB_RESOURCE, [MDB_RESOURCE])


@pytest.mark.e2e_replica_set_tls_managedcert_cleanup_on_delete
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_cleanup_on_delete
def test_replica_set_running(mdb: MongoDB, namespace: str):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=600)

    for name in leaf_secret_names_for(MDB_RESOURCE, [MDB_RESOURCE]):
        first_leaf_certificates[name] = read_secret(namespace, name)["tls.crt"]

    assert_all_exist(dependent_k8s_resources(), namespace)


@pytest.mark.e2e_replica_set_tls_managedcert_cleanup_on_delete
# Deleting the resource removes everything the feature created for it: every object in the list has to
# be gone before the test passes.
def test_cleanup(mdb: MongoDB, namespace: str):
    mdb.delete()

    def everything_is_gone() -> bool:
        objects = [("mongodb", MDB_RESOURCE)] + dependent_k8s_resources()
        return all(is_gone(kind, name, namespace) for kind, name in objects)

    # Everything is in the operator's cluster and carries the resource as its owner, so Kubernetes
    # deletes it together with the resource.
    wait_until(everything_is_gone, timeout=300)


@pytest.mark.e2e_replica_set_tls_managedcert_cleanup_on_delete
def test_a_resource_with_the_same_name_gets_fresh_certificates(namespace: str, custom_mdb_version: str):
    # A new object: the one the fixture returns still refers to the deleted resource, so updating it
    # would patch a resource that is not there.
    mdb = managed_cert_replica_set(MDB_RESOURCE, namespace, custom_mdb_version, {})
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)

    # The feature created its whole set again for the new resource, not only the certificates below.
    assert_all_exist(dependent_k8s_resources(), namespace)

    for name in leaf_secret_names_for(MDB_RESOURCE, [MDB_RESOURCE]):
        assert (
            read_secret(namespace, name)["tls.crt"] != first_leaf_certificates[name]
        ), f"{name} still holds the certificate of the deleted deployment, so cert-manager found the secret it left behind"
