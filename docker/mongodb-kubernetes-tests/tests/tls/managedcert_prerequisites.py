"""The operator-managed certificates prerequisites, in the order an operator meets them.

The operator decides once, at startup, whether it may use cert-manager: the Certificate and Issuer
CRDs have to be registered and it has to be allowed to manage them. This module walks the three
states in one deployment: cert-manager missing, cert-manager present but no RBAC, and both in place.

A resource that enables security.managedCertificate has to fail with the message naming the missing
piece in the first two states, and recover to Running in the third, which is also what proves that
fixing the prerequisites and restarting the operator heals a resource that failed before.

The replica set and the sharded cluster run through the same phases, because they are separate
controllers with separate checks.
"""

import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_default_operator,
    get_operator_installation_config,
    install_cert_manager,
)
from tests.tls.managedcert_helper import managed_cert_replica_set

RS_RESOURCE = "test-managedcert-prereq-rs"
SC_RESOURCE = "test-managedcert-prereq-sc"
PLAIN_RESOURCE = "test-managedcert-prereq-plain"


def _install_operator(namespace: str, managed_certificates: bool) -> Operator:
    """Installs the operator, with the cert-manager RBAC when asked. The chart grants that RBAC from
    the security.managedCertificate.enabled value."""
    config = get_operator_installation_config(namespace)
    if managed_certificates:
        config["security.managedCertificate.enabled"] = "true"
    return get_default_operator(namespace, config)


@pytest.fixture(scope="module")
def managed_rs(namespace: str, custom_mdb_version: str) -> MongoDB:
    return managed_cert_replica_set(RS_RESOURCE, namespace, custom_mdb_version, {})


@pytest.fixture(scope="module")
def managed_sc(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-tls-sc-managedcert.yaml"), name=SC_RESOURCE, namespace=namespace)
    resource["spec"]["security"]["managedCertificate"] = {}
    try_load(resource)
    return resource


@pytest.fixture(scope="module")
def plain_rs(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("test-no-tls-no-status.yaml"), name=PLAIN_RESOURCE, namespace=namespace)
    resource.set_version(custom_mdb_version)
    try_load(resource)
    return resource


@pytest.mark.e2e_managed_certificate_prerequisites
def test_cert_manager_missing(namespace: str, managed_rs: MongoDB, managed_sc: MongoDB):
    # The operator is installed without the cert-manager RBAC, and cert-manager is not installed.
    _install_operator(namespace, managed_certificates=False)

    managed_rs.update()
    managed_rs.assert_reaches_phase(Phase.Failed, msg_regexp="cert-manager is not installed", timeout=300)

    managed_sc.update()
    managed_sc.assert_reaches_phase(Phase.Failed, msg_regexp="cert-manager is not installed", timeout=300)


@pytest.mark.e2e_managed_certificate_prerequisites
def test_other_resources_are_unaffected(plain_rs: MongoDB):
    # A resource that does not use managedCertificate reconciles normally. Running it while the
    # managed resources are failed proves a missing cert-manager does not wedge the operator.
    plain_rs.update()
    plain_rs.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_managed_certificate_prerequisites
def test_cert_manager_without_rbac(namespace: str, managed_rs: MongoDB, managed_sc: MongoDB):
    # cert-manager is installed now, but the operator still has no RBAC for it. The operator reads
    # its prerequisites once at startup, so it has to restart before it sees cert-manager.
    install_cert_manager(cluster_client=get_central_cluster_client(), cluster_name=get_central_cluster_name())
    _install_operator(namespace, managed_certificates=False).restart_operator_deployment()

    managed_rs.assert_reaches_phase(Phase.Failed, msg_regexp="not allowed to", timeout=600)
    managed_sc.assert_reaches_phase(Phase.Failed, msg_regexp="not allowed to", timeout=600)


@pytest.mark.e2e_managed_certificate_prerequisites
def test_rbac_granted_recovers(namespace: str, managed_rs: MongoDB, managed_sc: MongoDB):
    # Installing the operator with the chart value grants the cert-manager RBAC. The operator has to
    # restart again to pick it up, and the resources that failed before have to recover.
    _install_operator(namespace, managed_certificates=True).restart_operator_deployment()

    managed_rs.assert_reaches_phase(Phase.Running, timeout=1800)
    managed_sc.assert_reaches_phase(Phase.Running, timeout=1800)
