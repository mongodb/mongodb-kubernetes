"""Deleting a multi-cluster replica set that uses operator-managed certificates deletes what the feature
created for it, in the operator's cluster and in every member cluster, so a resource created afterwards
with the same name is issued fresh certificates.

The two clusters are cleaned up by different routes. In the operator's cluster the Certificates,
issuers, issued secrets and CA bundles carry the resource as their owner, so Kubernetes deletes them
with it. The member clusters cannot point at a resource in another cluster, so the -pem secrets the
pods mount and the copies of the CA bundles there carry the resource's labels instead, and the
operator deletes them when the resource is deleted.
"""

from typing import List

import kubernetes
from kubetester import read_secret, wait_until
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.tls.managedcert_helper import (
    assert_all_exist,
    is_gone,
    leaf_secret_names_for,
    managed_cert_multi_cluster_objects,
)

MDB_RESOURCE = "multi-cluster-rs-managedcert-cleanup"
MEMBER_CLUSTER_MEMBERS = [1, 1, 1]

# The leaf certificates the first deployment was issued, to compare with the ones the second gets.
# They are issued in the operator's cluster, whichever cluster the pods run in.
first_leaf_certificates: dict[str, str] = {}


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    """Installs the operator with operator-managed certificates enabled, after cert-manager.

    The operator decides once, at startup, whether it may use cert-manager, so cert-manager has to be
    installed first (the cert_manager dependency) and the operator needs the cert-manager RBAC, which
    in multi-cluster mode the kubectl-mongodb setup tool creates. This overrides the fixture of the
    same name in tests/conftest.py for this module only.
    """
    operator_installation_config = get_multi_cluster_operator_installation_config(namespace)
    operator_installation_config["security.managedCertificate.enabled"] = "true"
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        operator_installation_config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=True,
    )


def managed_cert_multi_cluster_replica_set(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    mdb_version: str,
) -> MongoDBMulti:
    """A TLS multi-cluster replica set with X509 member and internal auth, using operator-managed
    certificates, one member in each member cluster.

    It runs without persistent storage, so the second deployment starts from empty disks and nothing of
    the first one is left but what this test checks for.
    """
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, list(MEMBER_CLUSTER_MEMBERS))
    # No issuer for either category: the operator self-signs both the server and the client CAs.
    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "managedCertificate": {},
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "internalCluster": "X509",
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    return managed_cert_multi_cluster_replica_set(
        central_cluster_client, namespace, member_cluster_names, custom_mdb_version
    )


def dependent_k8s_resources() -> tuple[list[tuple[str, str]], list[tuple[str, str]]]:
    """Everything the feature creates for the resource: in the operator's cluster, and in each member
    cluster."""
    return managed_cert_multi_cluster_objects(MDB_RESOURCE, [MDB_RESOURCE])


@mark.e2e_managed_certificate_multi_cluster_rs_cleanup_on_delete
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_managed_certificate_multi_cluster_rs_cleanup_on_delete
def test_mongodb_multi_reaches_running(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)

    for name in leaf_secret_names_for(MDB_RESOURCE, [MDB_RESOURCE]):
        first_leaf_certificates[name] = read_secret(namespace, name, central_cluster_client)["tls.crt"]

    operator_cluster_objects, member_cluster_objects = dependent_k8s_resources()
    assert_all_exist(operator_cluster_objects, namespace, central_cluster_client)
    for member_cluster_client in member_cluster_clients:
        assert_all_exist(member_cluster_objects, namespace, member_cluster_client.api_client)


@mark.e2e_managed_certificate_multi_cluster_rs_cleanup_on_delete
# Deleting the resource removes everything the feature created for it, in the operator's cluster and in
# every member cluster: every object in the list has to be gone in all of them.
def test_cleanup(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.delete()
    operator_cluster_objects, member_cluster_objects = dependent_k8s_resources()

    def everything_is_gone() -> bool:
        # In the operator's cluster, Kubernetes deletes what carries the resource as its owner.
        objects = [("mongodbmulticluster", MDB_RESOURCE)] + operator_cluster_objects
        if not all(is_gone(kind, name, namespace, central_cluster_client) for kind, name in objects):
            return False
        # In the member clusters, the operator deletes what carries the resource's labels.
        return all(
            is_gone(kind, name, namespace, member_cluster_client.api_client)
            for member_cluster_client in member_cluster_clients
            for kind, name in member_cluster_objects
        )

    wait_until(everything_is_gone, timeout=300)


@mark.e2e_managed_certificate_multi_cluster_rs_cleanup_on_delete
def test_a_resource_with_the_same_name_gets_a_fresh_certificate(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
    custom_mdb_version: str,
):
    # A new object: the one the fixture returns still refers to the deleted resource, so updating it
    # would patch a resource that is not there.
    mongodb_multi = managed_cert_multi_cluster_replica_set(
        central_cluster_client, namespace, member_cluster_names, custom_mdb_version
    )
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)

    # The feature created its whole set again for the new resource, in the operator's cluster and in
    # every member cluster, not only the certificates below.
    operator_cluster_objects, member_cluster_objects = dependent_k8s_resources()
    assert_all_exist(operator_cluster_objects, namespace, central_cluster_client)
    for member_cluster_client in member_cluster_clients:
        assert_all_exist(member_cluster_objects, namespace, member_cluster_client.api_client)

    for name in leaf_secret_names_for(MDB_RESOURCE, [MDB_RESOURCE]):
        assert (
            read_secret(namespace, name, central_cluster_client)["tls.crt"] != first_leaf_certificates[name]
        ), f"{name} still holds the certificate of the deleted deployment, so cert-manager found the secret it left behind"
