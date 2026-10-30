"""The operator-managed certificates prerequisites for a multi-cluster replica set, in order.

The same three states as the single-cluster module (tests/tls/managedcert_prerequisites.py), but for
the MongoDBMultiCluster controller and its own operator install: cert-manager missing, cert-manager
present but no RBAC, and both in place.

In multi-cluster mode the operator RBAC does not come from the chart. The kubectl-mongodb setup tool
creates it, and it only creates the cert-manager part when asked, which is what the last phase does.
"""

from typing import List

import kubernetes
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
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
    install_cert_manager,
)
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE = "test-mc-rs-managedcert-prereq"


def _install_operator(namespace: str, managed_certificate_roles: bool) -> Operator:
    """Installs the multi-cluster operator, asking the kubectl-mongodb setup tool for the cert-manager
    RBAC when asked."""
    config = get_multi_cluster_operator_installation_config(namespace)
    if managed_certificate_roles:
        config["security.managedCertificate.enabled"] = "true"
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=managed_certificate_roles,
    )


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])
    resource["spec"]["security"] = {
        "tls": {"enabled": True},
        "managedCertificate": {},
        "authentication": {"enabled": True, "modes": ["X509"], "internalCluster": "X509"},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.e2e_managed_certificate_multi_cluster_rs_prerequisites
def test_cert_manager_missing(namespace: str, mongodb_multi: MongoDBMulti):
    # The operator is installed without the cert-manager RBAC, and cert-manager is not installed.
    _install_operator(namespace, managed_certificate_roles=False)

    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(
        Phase.Failed,
        msg_regexp="It seems cert-manager is not installed. Install it, grant the operator the cert-manager RBAC",
        timeout=300,
    )


@mark.e2e_managed_certificate_multi_cluster_rs_prerequisites
def test_cert_manager_without_rbac(namespace: str, mongodb_multi: MongoDBMulti):
    # cert-manager is installed now, but the operator still has no RBAC for it. The operator reads
    # its prerequisites once at startup, so it has to restart before it sees cert-manager.
    install_cert_manager(cluster_client=get_central_cluster_client(), cluster_name=get_central_cluster_name())
    _install_operator(namespace, managed_certificate_roles=False).restart_operator_deployment()

    mongodb_multi.assert_reaches_phase(Phase.Failed, msg_regexp="the operator is not allowed to", timeout=600)


@mark.e2e_managed_certificate_multi_cluster_rs_prerequisites
def test_rbac_granted_recovers(namespace: str, mongodb_multi: MongoDBMulti):
    # Installing the operator with the managed-certificate roles granted lets the operator use
    # cert-manager. It has to restart to pick that up, and the resource that failed before has to
    # recover.
    _install_operator(namespace, managed_certificate_roles=True).restart_operator_deployment()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1800)
