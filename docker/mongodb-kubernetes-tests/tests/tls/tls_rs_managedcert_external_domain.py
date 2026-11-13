"""A TLS replica set with operator-managed certificates and an external domain.

With x509 internal cluster authentication the operator issues a clusterfile: the client certificate a
member presents when it connects to the other members. It carries no hostnames on purpose, the members
recognise each other by its subject. The member certificate, the one a client sees, carries every name
the deployment is reached by, and with an external domain that includes <pod>.<externalDomain>, which
is also how the members address each other.
"""

import pytest
from cryptography import x509
from kubetester import read_secret
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import default_external_domain, external_domain_fqdns, update_coredns_hosts
from tests.tls.managedcert_helper import leaf_secret_names, managed_cert_replica_set, managed_certificate_operator

MDB_RESOURCE = "test-tls-rs-managedcert-external-domain"


@pytest.fixture(scope="module")
def operator(cert_manager: str, namespace: str) -> Operator:
    # cert_manager is a dependency, not used here: cert-manager has to be installed before the
    # operator starts, because the operator decides once, at startup, whether it may use cert-manager.
    return managed_certificate_operator(namespace)


@pytest.fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    # managed_cert_replica_set loads the resource when it already exists, so the external access is set
    # after it: a second load would replace the spec set here, and on a re-run the resource would be
    # updated without the external domain.
    resource = managed_cert_replica_set(MDB_RESOURCE, namespace, custom_mdb_version, {})
    resource["spec"]["externalAccess"] = {"externalDomain": default_external_domain()}
    return resource


@pytest.mark.e2e_replica_set_tls_managedcert_external_domain
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@pytest.mark.e2e_replica_set_tls_managedcert_external_domain
def test_update_coredns():
    # The external domain resolves to the LoadBalancer addresses the kind cluster hands out, the same
    # way tests/tls/tls_replica_set_process_hostnames.py sets it up.
    hosts = [
        ("172.18.255.200", f"{MDB_RESOURCE}-0.{default_external_domain()}"),
        ("172.18.255.201", f"{MDB_RESOURCE}-1.{default_external_domain()}"),
        ("172.18.255.202", f"{MDB_RESOURCE}-2.{default_external_domain()}"),
    ]
    update_coredns_hosts(hosts)


@pytest.mark.e2e_replica_set_tls_managedcert_external_domain
def test_replica_set_running(mdb: MongoDB):
    # Reaching Running is the proof that the clusterfile is accepted: the members present it to each
    # other over the external hostnames, and the operator had to check it before the rollout.
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_replica_set_tls_managedcert_external_domain
def test_member_certificate_covers_the_external_domain(namespace: str):
    member, _, _ = leaf_secret_names(MDB_RESOURCE)
    sans = _dns_sans(read_secret(namespace, member)["tls.crt"])

    for hostname in external_domain_fqdns(MDB_RESOURCE, 3):
        assert hostname in sans, f"the member certificate has to be usable for {hostname}"
    assert not any(san.startswith("*") for san in sans), "the certificate lists the real hostnames, not a wildcard"


@pytest.mark.e2e_replica_set_tls_managedcert_external_domain
def test_clusterfile_carries_no_hostnames(namespace: str):
    # The members recognise each other by the subject of this certificate, so it needs no hostnames.
    _, clusterfile, _ = leaf_secret_names(MDB_RESOURCE)
    assert _dns_sans(read_secret(namespace, clusterfile)["tls.crt"]) == []


@pytest.mark.e2e_replica_set_tls_managedcert_external_domain
def test_members_are_addressed_by_the_external_domain(mdb: MongoDB):
    processes = mdb.get_automation_config_tester().get_replica_set_processes(mdb.name)
    hostnames = [process["hostname"] for process in processes]
    assert hostnames == external_domain_fqdns(mdb.name, mdb.get_members())


def _dns_sans(cert_pem: str) -> list[str]:
    """The DNS names of a certificate, empty when it carries no hostnames at all."""
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    try:
        extension = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    except x509.ExtensionNotFound:
        # A certificate with no hostnames has no such extension, rather than an empty one.
        return []
    return extension.value.get_values_for_type(x509.DNSName)
